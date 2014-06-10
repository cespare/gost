package main

import (
	"math"
	"testing"

	. "launchpad.net/gocheck"
)

// ----------- Gocheck config ----------------

func Test(t *testing.T) { TestingT(t) }

type BufferedStatsSuite struct{}

var _ = Suite(&BufferedStatsSuite{})

// ----------- Setup ----------------

func (*BufferedStatsSuite) SetUpTest(c *C) {
	conf = &Conf{
		FlushIntervalMS: 2000,
	}
}

// ----------- Tests ----------------

func (*BufferedStatsSuite) TestCounter(c *C) {
	s := NewBufferedStats()
	s.AddCount("foo", 1)
	s.AddCount("foo", 3)
	r := s.computeDerived()
	c.Check(r["count"]["foo"], approx, 4.0)
	c.Check(r["rate"]["foo"], approx, 2.0)
}

func (*BufferedStatsSuite) TestGauge(c *C) {
	s := NewBufferedStats()
	s.SetGauge("foo", 10)
	s.SetGauge("foo", 20)
	r := s.computeDerived()
	c.Check(r["gauge"]["foo"], approx, 20.0)
}

func (*BufferedStatsSuite) TestSet(c *C) {
	s := NewBufferedStats()
	s.AddSetItem("foo", 123)
	s.AddSetItem("foo", 123)
	s.AddSetItem("foo", 456)
	r := s.computeDerived()
	c.Check(r["set"]["foo"], approx, 2.0)
}

func (*BufferedStatsSuite) TestTimer(c *C) {
	s := NewBufferedStats()
	s.RecordTimer("foo", 100.0)
	s.RecordTimer("foo", 600.0)
	s.RecordTimer("foo", 200.0)
	r := s.computeDerived()
	c.Check(r["timer.count"]["foo"], approx, 3.0)
	c.Check(r["timer.rate"]["foo"], approx, 1.5)
	c.Check(r["timer.sum"]["foo"], approx, 900.0)
	c.Check(r["timer.mean"]["foo"], approx, 300.0)
	c.Check(r["timer.stdev"]["foo"], approx, math.Sqrt((200.0*200.0+300.0*300.0+100.0*100.0)/3))
	c.Check(r["timer.min"]["foo"], approx, 100.0)
	c.Check(r["timer.max"]["foo"], approx, 600.0)
	c.Check(r["timer.median"]["foo"], approx, 200.0)

	s = NewBufferedStats()
	s.RecordTimer("bar", 100.0)
	s.RecordTimer("bar", 200.0)
	r = s.computeDerived()
	c.Check(r["timer.median"]["bar"], approx, 150.0)
}
