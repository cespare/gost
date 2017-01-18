package main

import (
	"math"
	"testing"
	"time"
)

func TestBufferedStatsCounter(t *testing.T) {
	s := NewBufferedStats(2000)
	s.AddCount("foo", 1)
	s.AddCount("foo", 3)
	r := s.computeDerived()
	approx(t, r["count"]["foo"], 4.0)
	approx(t, r["rate"]["foo"], 2.0)
}

func TestBufferedStatsGauge(t *testing.T) {
	s := NewBufferedStats(2000)
	s.SetGauge("foo", 10)
	s.SetGauge("foo", 20)
	s.SetGaugeExpiration("foo", 10*time.Millisecond)
	r := s.computeDerived()
	approx(t, r["gauge"]["foo"], 20.0)
	time.Sleep(20 * time.Millisecond)
	s.Clear(true)
	r = s.computeDerived()
	if _, ok := r["gauge"]["foo"]; ok {
		t.Fatal("gauges not cleared")
	}
}

func TestBufferedStatsSet(t *testing.T) {
	s := NewBufferedStats(2000)
	s.AddSetItem("foo", 123)
	s.AddSetItem("foo", 123)
	s.AddSetItem("foo", 456)
	r := s.computeDerived()
	approx(t, r["set"]["foo"], 2.0)
}

func TestBufferedStatsTimer(t *testing.T) {
	s := NewBufferedStats(2000)
	s.RecordTimer("foo", 100.0)
	s.RecordTimer("foo", 600.0)
	s.RecordTimer("foo", 200.0)
	r := s.computeDerived()
	approx(t, r["timer.count"]["foo"], 3.0)
	approx(t, r["timer.rate"]["foo"], 1.5)
	approx(t, r["timer.sum"]["foo"], 900.0)
	approx(t, r["timer.mean"]["foo"], 300.0)
	approx(t, r["timer.stdev"]["foo"], math.Sqrt((200.0*200.0+300.0*300.0+100.0*100.0)/3))
	approx(t, r["timer.min"]["foo"], 100.0)
	approx(t, r["timer.max"]["foo"], 600.0)
	approx(t, r["timer.median"]["foo"], 200.0)

	s = NewBufferedStats(2000)
	s.RecordTimer("bar", 100.0)
	s.RecordTimer("bar", 200.0)
	r = s.computeDerived()
	approx(t, r["timer.median"]["bar"], 150.0)
}
