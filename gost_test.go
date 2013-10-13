package main

import (
	"fmt"
	"io/ioutil"
	"log"
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
	rec                          = &recorder{}
	testUDPConn                  *net.UDPConn
	testForwardListener          net.Listener
	when                         = time.Time{}
	aggregateFlushChan           = make(chan time.Time)
	aggregateForwardedFlushChan  = make(chan time.Time)
	aggregateForwardingFlushChan = make(chan time.Time)
	flushers                     int
)

func (*GostSuite) SetUpSuite(c *C) {
	// Stub out control functions
	now = func() time.Time { return when }
	aggregateFlushTicker = func() <-chan time.Time { return aggregateFlushChan }
	aggregateForwardedFlushTicker = func() <-chan time.Time { return aggregateForwardedFlushChan }
	aggregateForwardingFlushTicker = func() <-chan time.Time { return aggregateForwardingFlushChan }

	// Start workers
	go flush()
	go aggregate()
	// Don't want to see bad output lines (we could capture and inspect these if we want to test them later).
	log.SetOutput(ioutil.Discard)

	var err error
	testForwardListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.Fatal(err)
	}
	forwardingIncoming = make(chan *Stat, incomingQueueSize)
	go flushForwarding()
	go aggregateForwarding()
	go aggregateForwarded()
	go forwardServer(testForwardListener)

	// Bind to any port
	if err := debugServer.Start(0); err != nil {
		c.Fatal(err)
	}

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

func (*GostSuite) TearDownSuite(c *C) {
	testUDPConn.Close()
	testForwardListener.Close()
}

func (*GostSuite) SetUpTest(c *C) {
	rec.Start()
	conf = &Conf{
		GraphiteAddr:             rec.Addr,
		ForwardingAddr:           testForwardListener.Addr().String(),
		ForwardedNamespace:       "global",
		ClearStatsBetweenFlushes: true,
		FlushIntervalMS:          2000, // Fake
		Namespace:                "com.example",
	}
}

func (*GostSuite) TearDownTest(c *C) {
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
	messages chan []byte
}

// waitForMessage flushes the aggregator and returns the collected messages.
func (r *recorder) waitForMessage() *graphiteMessage {
	// Ensure the aggregator has time to collect all the messages we've sent in.
	time.Sleep(time.Millisecond)
	aggregateFlushChan <- when
	return parseRawGraphiteMessage(<-r.messages)
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
	r.messages = make(chan []byte)
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
	r.messages <- msg
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
			c.Fatal("No graphite key found: ", key)
		}
		c.Check(parsed.Value, approx, test.Value)
	}
}

// ----------- Tests ----------------

func (*GostSuite) TestCounters(c *C) {
	sendGostMessages(c, "foobar:3|c", "foobar:5|c", "baz:2|c|@0.1", "baz:4|c|@0.1")
	checkAllApprox(c, []testCase{
		{"foobar.count", 8.0},
		{"foobar.rate", 4.0},
		{"baz.count", 60.0},
		{"baz.rate", 30.0},
	})
}

func (*GostSuite) TestTimers(c *C) {
	sendGostMessages(c, "foobar:100|ms", "foobar:100|ms", "foobar:400|ms", "baz:500|ms")
	checkAllApprox(c, []testCase{
		{"foobar.timer.count", 3.0},
		{"foobar.timer.rate", 1.5},
		{"foobar.timer.min", 100.0},
		{"foobar.timer.max", 400.0},
		{"baz.timer.count", 1.0},
	})
}

func (*GostSuite) TestGauges(c *C) {
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

func (*GostSuite) TestSets(c *C) {
	sendGostMessages(c, "foobar:123|s", "foobar:234|s", "foobar:123|s", "baz:456|s")
	checkAllApprox(c, []testCase{
		{"foobar.set", 2.0},
		{"baz.set", 1.0},
	})
}

func (*GostSuite) TestMetaStats(c *C) {
	sendGostMessages(c, "foobar:2|c", "foobar:3|g", "foobar:asdf|s")
	sendGostMessages(c, "baz:300|g")
	sendGostMessages(c, "baz:300|asdfasdf")
	checkAllApprox(c, []testCase{
		{"gost.bad_messages_seen.count", 2.0},
		{"gost.packets_received.count", 5.0},
		// foobar/count (2), foobar/gauge (1), baz/gauge (1), gost.packets_received/count (2),
		// gost.bad_messages_seen/count (2), gost.distinct_metrics_flushed/gauge (1)
		// total = 9
		{"gost.distinct_metrics_flushed.gauge", 9.0},
	})
}

func (*GostSuite) TestWithStatClearing(c *C) {
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

func (*GostSuite) TestWithoutStatClearing(c *C) {
	conf.ClearStatsBetweenFlushes = false
	sendGostMessages(c, "a:1|c", "b:2|ms", "c:3|g", "d:4|s")
	time.Sleep(time.Millisecond)
	aggregateFlushChan <- when
	<-rec.messages
	sendGostMessages(c, "foobar:2|c")
	time.Sleep(time.Millisecond)
	aggregateFlushChan <- when
	msg := parseRawGraphiteMessage(<-rec.messages)
	c.Check(msg.Parsed["com.example.a.count"].Value, approx, 0.0)
	c.Check(msg.Parsed["com.example.a.rate"].Value, approx, 0.0)
	c.Check(msg.Parsed["com.example.b.timer.count"], IsNil)
	c.Check(msg.Parsed["com.example.c.gauge"].Value, approx, 3.0)
	c.Check(msg.Parsed["com.example.d.set"].Value, approx, 0.0)
	c.Check(msg.Parsed["com.example.foobar.count"].Value, approx, 2.0)
}

func (*GostSuite) TestSanitization(c *C) {
	allChars := []byte{}
	for i := 33; i <= 126; i++ {
		c := byte(i)
		switch c {
		case '*', '/', ':', '<', '>', '[', ']', '{', '}':
			continue
		}
		allChars = append(allChars, c)
	}
	sendGostMessages(c,
		fmt.Sprintf("%s:1|c", allChars),
		"föo\tbar:1|c",  // Non-printable or non-ascii is removed
		"foo bar:1|c",   // spaces changed to _
		"foo/bar:1|c",   // / changed to -
		"rem*ove1:1|c",  // * removed
		"<remove2>:1|c", // < > removed
		"[remove3]:1|c", // [ ] removed
		"{remove4}:1|c", // { } removed
	)
	checkAllApprox(c, []testCase{
		{fmt.Sprintf("%s.count", allChars), 1.0},
		{"fobar.count", 1.0},
		{"foo_bar.count", 1.0},
		{"foo-bar.count", 1.0},
		{"remove1.count", 1.0},
		{"remove2.count", 1.0},
		{"remove3.count", 1.0},
		{"remove4.count", 1.0},
	})
}

func (*GostSuite) TestForwardedKeyParsing(c *C) {
	forwardingEnabled = true
	sendGostMessages(c, "f|foo:1|c", "f|f|bar:1|c", "f||baz:1|c", "quf|ux:1|c")
	checkAllApprox(c, []testCase{
		{"quf|ux.count", 1.0},
	})

	// Push the forwarded messages through
	aggregateForwardingFlushChan <- when
	time.Sleep(time.Millisecond)
	aggregateForwardedFlushChan <- when
	msg := parseRawGraphiteMessage(<-rec.messages)
	c.Check(msg.Parsed["global.foo.count"].Value, approx, 1.0)
	c.Check(msg.Parsed["global.f|bar.count"].Value, approx, 1.0)
	c.Check(msg.Parsed["global.|baz.count"].Value, approx, 1.0)
}

func (*GostSuite) TestNoForwarding(c *C) {
	forwardingEnabled = false
	sendGostMessages(c, "f|foo:1|c")
	checkAllApprox(c, []testCase{
		{"f|foo.count", 1.0},
	})
}

func (*GostSuite) TestSampleRates(c *C) {
	sendGostMessages(c, "a:1|c|@0.1", "b:1|c|@1.0", "c:1|c|@3.0", "d:1|c|@0.0", "e:1|c|@-0.5")
	msg := rec.waitForMessage()
	c.Check(msg.Parsed["com.example.a.count"].Value, approx, 10.0)
	c.Check(msg.Parsed["com.example.b.count"].Value, approx, 1.0)
	for _, key := range []string{"c", "d", "e"} {
		c.Check(msg.Parsed["com.example."+key+".count"], IsNil)
	}
}

func (*GostSuite) TestMultilineMessage(c *C) {
	sendGostMessages(c, "foobar:3|c\nfoobar:5|c\nbaz:200|g")
	checkAllApprox(c, []testCase{
		{"foobar.count", 8.0},
		{"foobar.rate", 4.0},
		{"baz.gauge", 200.0},
		{"gost.packets_received.count", 1.0},
		{"gost.packets_received.rate", 0.5},
	})
}
