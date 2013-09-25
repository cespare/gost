package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net"

	"strings"
	"sync"
	"testing"
	"time"

	. "launchpad.net/gocheck"
)

// ----------- Gocheck config ----------------

func Test(t *testing.T) { TestingT(t) }

type GostSuite struct{}

var _ = Suite(&GostSuite{})

// ----------- Setup ----------------

var (
	rec         = &recorder{}
	testUDPConn *net.UDPConn
	when        = time.Time{}
	flushChan   = make(chan time.Time)
)

func (s *GostSuite) SetUpSuite(c *C) {
	now = func() time.Time { return when }
	flushTicker = func() <-chan time.Time { return flushChan }
	go flush()
	go aggregate()
	// Don't want to see bad output lines (we could capture and inspect these if we want to test them later).
	log.SetOutput(ioutil.Discard)
}

func (s *GostSuite) SetUpTest(c *C) {
	rec.Start()
	conf = &Conf{
		GraphiteAddr:             rec.Addr,
		ClearStatsBetweenFlushes: true,
		FlushIntervalMS:          2000, // Fake
	}
	namespace = []string{"com", "example"}
	udp, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		c.Fatal(err)
	}
	testUDPConn, err = net.ListenUDP("udp", udp)
	if err != nil {
		c.Fatal(err)
	}
	go clientServer(testUDPConn)
}

func (s *GostSuite) TearDownTest(c *C) {
	testUDPConn.Close()
	rec.Close()
}

type graphiteValue struct {
	Value     float64
	Timestamp time.Time
}

type graphiteMessage struct {
	Raw    []byte
	Parsed map[string]*graphiteValue
}

func parseRawGraphiteMessage(raw []byte) *graphiteMessage {
	parsed := make(map[string]*graphiteValue)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		key := ""
		value := &graphiteValue{}
		var unixTime int64
		if _, err := fmt.Sscanf(line, "%s %f %d", &key, &value.Value, &unixTime); err != nil {
			panic("Not a graphite message: " + line)
		}
		value.Timestamp = time.Unix(unixTime, 0)
		parsed[key] = value
	}
	return &graphiteMessage{raw, parsed}
}

type recorder struct {
	W        *sync.WaitGroup
	Addr     string
	Listener net.Listener
	Messages chan *graphiteMessage
}

// waitForMessage spams flush until a message comes out. It's pretty hacky. It only works when clearing is
// turned off; otherwise you'll get lots of messages (not just the ones you intended).
func (r *recorder) waitForMessage() *graphiteMessage {
	ticker := time.NewTicker(time.Millisecond)
	for {
		select {
		case <-ticker.C:
			flushChan <- when
		case msg := <-r.Messages:
			return msg
		}
	}
}

func (r *recorder) Start() {
	r.W = &sync.WaitGroup{}
	// Listen on a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	r.Listener = listener
	r.Addr = listener.Addr().String()
	r.Messages = make(chan *graphiteMessage)
	go func() {
		for {
			c, err := r.Listener.Accept()
			if err != nil {
				return
			}
			go r.Handle(c)
		}
	}()
}

func (r *recorder) Handle(c net.Conn) {
	r.W.Add(1)
	defer r.W.Done()
	msg, err := ioutil.ReadAll(c)
	if err != nil {
		return
	}
	r.Messages <- parseRawGraphiteMessage(msg)
}

func (r *recorder) Close() {
	r.Listener.Close()
	r.W.Wait()
}

func sendGostMessages(c *C, msgs ...string) {
	conn, err := net.Dial("udp", testUDPConn.LocalAddr().String())
	if err != nil {
		c.Fatal(err)
	}
	defer conn.Close()
	for _, msg := range msgs {
		conn.Write([]byte(msg))
	}
}

// approx is a gocheck checker for approximate equality of floats.
// Note that approximate equality of floats is a fraught topic. This is a very naive comparison.
type approxChecker struct {
	*CheckerInfo
}

var approx = &approxChecker{
	&CheckerInfo{Name: "approx", Params: []string{"obtained", "expected"}},
}

func (c *approxChecker) Check(params []interface{}, names []string) (result bool, error string) {
	f1, ok1 := params[0].(float64)
	f2, ok2 := params[1].(float64)
	if !(ok1 && ok2) {
		return false, "must compare float64s"
	}
	if f1 == f2 {
		return true, ""
	} else if f1 > f2 {
		f1, f2 = f2, f1
	}
	delta := (f2 - f1) / f1
	if delta < 0.0001 { // Accept f2 up to 0.01% greater than f1
		return true, ""
	}
	return false, ""
}

type testCase struct {
	Key   string
	Value float64
}

func checkAllApprox(c *C, tests []testCase) {
	msg := rec.waitForMessage()
	for _, test := range tests {
		key := "com.example." + test.Key
		parsed := msg.Parsed[key]
		if parsed == nil {
			fmt.Println(string(msg.Raw))
			c.Fatal("No graphite key found:", key)
		}
		c.Check(parsed.Value, approx, test.Value)
	}
}

// ----------- Tests ----------------

func (s *GostSuite) TestCounters(c *C) {
	sendGostMessages(c, "foobar:3|c", "foobar:5|c", "baz:2|c|@0.1", "baz:4|c|@0.1")
	checkAllApprox(c, []testCase{
		{"foobar.count", 8.0},
		{"foobar.rate", 4.0},
		{"baz.count", 60.0},
		{"baz.rate", 30.0},
	})
}

func (s *GostSuite) TestTimers(c *C) {
	sendGostMessages(c, "foobar:100|ms", "foobar:100|ms", "foobar:400|ms", "baz:500|ms|@0.2")
	checkAllApprox(c, []testCase{
		{"foobar.timer.count", 3.0},
		{"foobar.timer.rate", 1.5},
		{"foobar.timer.min", 100.0},
		{"foobar.timer.max", 400.0},
		{"foobar.timer.median", 100.0},
		{"foobar.timer.mean", 200.0},
		{"foobar.timer.stdev", math.Sqrt((2*100.0*100.0 + 200.0*200.0) / 3)},

		{"baz.timer.count", 5.0},
		{"baz.timer.rate", 2.5},
		{"baz.timer.min", 500.0},
		{"baz.timer.max", 500.0},
		{"baz.timer.median", 500.0},
		{"baz.timer.mean", 500.0},
		{"baz.timer.stdev", 0.0},
	})
}

func (s *GostSuite) TestGauges(c *C) {
	sendGostMessages(c, "foobar:3|g")
	// Hack to ensure that the first foobar message gets processed before the second
	// TODO: find a better way
	time.Sleep(time.Millisecond)
	sendGostMessages(c, "foobar:4|g", "baz:1|g")

	checkAllApprox(c, []testCase{
		{"foobar.gauge", 4.0},
		{"baz.gauge", 1.0},
	})
}

func (s *GostSuite) TestSets(c *C) {
	sendGostMessages(c, "foobar:123|s", "foobar:234|s", "foobar:123|s", "baz:456|s")
	checkAllApprox(c, []testCase{
		{"foobar.set", 2.0},
		{"baz.set", 1.0},
	})
}

func (s *GostSuite) TestMetaStats(c *C) {
	sendGostMessages(c, "foobar:2|c", "foobar:3|g", "foobar:asdf|s")
	sendGostMessages(c, "baz:300|g")
	sendGostMessages(c, "baz:300|asdfasdf")
	checkAllApprox(c, []testCase{
		{"gost.bad_messages_seen.count", 2.0},
		{"gost.packets_received.count", 5.0},
	})
}

func (s *GostSuite) TestWithStatClearing(c *C) {
	conf.ClearStatsBetweenFlushes = true
	sendGostMessages(c, "a:1|c")
	sendGostMessages(c, "b:2|ms")
	sendGostMessages(c, "c:3|g")
	sendGostMessages(c, "d:4|s")
	rec.waitForMessage()
	sendGostMessages(c, "foobar:2|c")
	msg := rec.waitForMessage()
	for _, key := range []string{"a", "b", "c", "d"} {
		c.Check(msg.Parsed["com.example."+key+".count"], IsNil)
	}
	c.Check(msg.Parsed["com.example.foobar.count"].Value, approx, 2.0)
}

// TODO: this test is timing-sensitive and I've hacked around that with sleeps. Fix this.
func (s *GostSuite) TestWithoutStatClearing(c *C) {
	conf.ClearStatsBetweenFlushes = false
	sendGostMessages(c, "a:1|c")
	sendGostMessages(c, "b:2|ms")
	sendGostMessages(c, "c:3|g")
	sendGostMessages(c, "d:4|s")
	time.Sleep(time.Millisecond)
	flushChan <- when
	<-rec.Messages
	sendGostMessages(c, "foobar:2|c")
	time.Sleep(time.Millisecond)
	flushChan <- when
	msg := <-rec.Messages
	c.Check(msg.Parsed["com.example.a.count"].Value, approx, 0.0)
	c.Check(msg.Parsed["com.example.b.timer.count"], IsNil)
	c.Check(msg.Parsed["com.example.c.gauge"].Value, approx, 3.0)
	c.Check(msg.Parsed["com.example.d.set"].Value, approx, 0.0)
	c.Check(msg.Parsed["com.example.foobar.count"].Value, approx, 2.0)
}
