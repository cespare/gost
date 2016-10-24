package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() {
	log.SetOutput(ioutil.Discard)
}

// NOTE: this test suite does end-to-end testing of gost. It is not unit
// testing. The upside is that we get thorough test coverage of the system with
// its multiple components. The downsides:
// - It involves somewhat complex setup
// - It's timing-dependent (we sleep to give messages a chance to go through in
//   certain places)
// It's possible that these tests could be improved or tightened up.

type TestServer struct {
	s    *Server
	wait chan struct{} // wait on this until s closes

	udpConn         *net.UDPConn
	forwardListener net.Listener
	debugListener   net.Listener

	when                         time.Time // static time to return for now()
	aggregateFlushChan           chan time.Time
	aggregateForwardedFlushChan  chan time.Time
	aggregateForwardingFlushChan chan time.Time

	// The rest of the fields are used for recording the Graphite output.
	recorderWG       *sync.WaitGroup
	recorderAddr     string
	recorderListener net.Listener
	recorderMessages chan string
	recorderQuit     chan struct{}
}

func NewTestServer() *TestServer {
	conf := &Conf{
		ForwardedNamespace:       "global",
		ClearStatsBetweenFlushes: true,
		FlushIntervalMS:          2000,
		Namespace:                "com.example",
	}
	s := &TestServer{
		s:                            NewServer(conf),
		wait:                         make(chan struct{}),
		aggregateFlushChan:           make(chan time.Time),
		aggregateForwardedFlushChan:  make(chan time.Time),
		aggregateForwardingFlushChan: make(chan time.Time),
		recorderWG:                   new(sync.WaitGroup),
		recorderMessages:             make(chan string),
		recorderQuit:                 make(chan struct{}),
	}

	// Stub out control functions.
	s.s.now = func() time.Time { return s.when }
	s.s.aggregateFlushTicker = func() <-chan time.Time { return s.aggregateFlushChan }
	s.s.aggregateForwardedFlushTicker = func() <-chan time.Time { return s.aggregateForwardedFlushChan }
	s.s.aggregateForwardingFlushTicker = func() <-chan time.Time { return s.aggregateForwardingFlushChan }

	// Set up listeners that will be passed into the server under test.
	var err error
	s.forwardListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s.debugListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	udp, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s.udpConn, err = net.ListenUDP("udp", udp)
	if err != nil {
		panic(err)
	}

	// Start the fake Graphite recorder.
	s.recorderListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s.recorderAddr = s.recorderListener.Addr().String()
	// This goroutine will exit when the listener is closed.
	go func() {
		for {
			c, err := s.recorderListener.Accept()
			if err != nil {
				return
			}
			s.recorderWG.Add(1)
			go func() {
				defer s.recorderWG.Done()
				c := c
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					msg := scanner.Text()
					select {
					case <-s.recorderQuit:
						return
					case s.recorderMessages <- msg:
					}
				}
				// Deliberately ignore scanner.Err().
			}()
		}
	}()

	// Backfill some conf fields.
	s.s.conf.GraphiteAddrs = []string{s.recorderAddr}
	s.s.conf.ForwardingAddrs = []string{s.forwardListener.Addr().String()}

	return s
}

func (s *TestServer) Start() {
	go func() {
		s.s.Listen(s.udpConn, s.forwardListener, s.debugListener)
		s.wait <- struct{}{}
	}()
}

func NewStartedTestServer() *TestServer {
	s := NewTestServer()
	s.Start()
	return s
}

func (s *TestServer) Close() {
	close(s.recorderQuit)
	s.recorderListener.Close()
	close(s.s.quit)
	s.udpConn.Close()
	s.forwardListener.Close()
	s.recorderWG.Wait()
	<-s.wait
}

func (s *TestServer) WaitForMessage() *graphiteMessages {
	// Ensure the aggregator has time to collect all the messages we've sent in.
	time.Sleep(10 * time.Millisecond)
	s.aggregateFlushChan <- s.when
	timer := time.NewTimer(10 * time.Millisecond)
	var messages []string
loop:
	for {
		select {
		case <-timer.C:
			break loop
		case msg := <-s.recorderMessages:
			messages = append(messages, msg)
		}
	}
	return parseRawGraphiteMessages(messages)
}

func (s *TestServer) SendGostMessages(t testing.TB, msgs ...string) {
	conn, err := net.Dial("udp", s.udpConn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, msg := range msgs {
		conn.Write([]byte(msg))
	}
}

type testCase struct {
	Key   string
	Value float64
}

func (s *TestServer) CheckAllApprox(t testing.TB, tests []testCase) {
	msg := s.WaitForMessage()
	for _, test := range tests {
		key := "com.example." + test.Key
		parsed := msg.Parsed[key]
		if parsed == nil {
			t.Logf("%s\n", msg.Raw)
			t.Fatal("No graphite key found: ", key)
		}
		approx(t, parsed.Value, test.Value)
	}
}

func TestCounters(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:3|c", "foobar:5|c", "baz:2|c|@0.1", "baz:4|c|@0.1")
	s.CheckAllApprox(t, []testCase{
		{"foobar.count", 8.0},
		{"foobar.rate", 4.0},
		{"baz.count", 60.0},
		{"baz.rate", 30.0},
	})
}

func TestTimers(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:100|ms", "foobar:100|ms", "foobar:400|ms", "baz:500|ms")
	s.CheckAllApprox(t, []testCase{
		{"foobar.timer.count", 3.0},
		{"foobar.timer.rate", 1.5},
		{"foobar.timer.min", 100.0},
		{"foobar.timer.max", 400.0},
		{"baz.timer.count", 1.0},
	})
}

func TestGauges(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:3|g")
	// Hack to ensure that the first foobar message gets processed before the second
	time.Sleep(time.Millisecond)
	s.SendGostMessages(t, "foobar:4|g", "baz:1|g")

	s.CheckAllApprox(t, []testCase{
		{"foobar.gauge", 4.0},
		{"baz.gauge", 1.0},
	})
}

func TestSets(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:123|s", "foobar:234|s", "foobar:123|s", "baz:456|s")
	s.CheckAllApprox(t, []testCase{
		{"foobar.set", 2.0},
		{"baz.set", 1.0},
	})
}

func TestMetaStats(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:2|c", "foobar:3|g", "foobar:asdf|s")
	s.SendGostMessages(t, "baz:300|g")
	s.SendGostMessages(t, "baz:300|asdfasdf")
	s.CheckAllApprox(t, []testCase{
		{"gost.errors.bad_message.count", 2.0},
		{"gost.packets_received.count", 5.0},
		// server_start (2), foobar/count (2), foobar/gauge (1),
		// baz/gauge (1), gost.packets_received/count (2),
		// gost.bad_messages_seen/count (2),
		// gost.distinct_metrics_flushed/gauge (1)
		// total = 11
		{"gost.distinct_metrics_flushed.gauge", 11.0},
	})
}

func TestWithStatClearing(t *testing.T) {
	s := NewTestServer()
	s.s.conf.ClearStatsBetweenFlushes = true
	s.Start()
	defer s.Close()
	s.SendGostMessages(t, "a:1|c")
	s.SendGostMessages(t, "b:2|ms")
	s.SendGostMessages(t, "c:3|g")
	s.SendGostMessages(t, "d:4|s")
	s.WaitForMessage()
	s.SendGostMessages(t, "foobar:2|c")
	msg := s.WaitForMessage()
	for _, key := range []string{"a", "b", "c", "d"} {
		_, ok := msg.Parsed["com.example."+key+".count"]
		equals(t, ok, false)
	}
	approx(t, msg.Parsed["com.example.foobar.count"].Value, 2.0)
}

func TestWithoutStatClearing(t *testing.T) {
	s := NewTestServer()
	s.s.conf.ClearStatsBetweenFlushes = false
	s.Start()
	defer s.Close()

	s.SendGostMessages(t, "a:1|c", "b:2|ms", "c:3|g", "d:4|s")
	_ = s.WaitForMessage()

	s.SendGostMessages(t, "foobar:2|c")
	msg := s.WaitForMessage()
	approx(t, msg.Parsed["com.example.a.count"].Value, 0.0)
	approx(t, msg.Parsed["com.example.a.rate"].Value, 0.0)
	equals(t, msg.Parsed["com.example.b.timer.count"], nil)
	approx(t, msg.Parsed["com.example.c.gauge"].Value, 3.0)
	approx(t, msg.Parsed["com.example.d.set"].Value, 0.0)
	approx(t, msg.Parsed["com.example.foobar.count"].Value, 2.0)
}

func TestSanitization(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	allChars := []byte{}
	for i := 33; i <= 126; i++ {
		c := byte(i)
		switch c {
		case '*', '/', ':', '<', '>', '[', ']', '{', '}':
			continue
		}
		allChars = append(allChars, c)
	}
	s.SendGostMessages(t,
		fmt.Sprintf("%s:1|c", allChars),
		"föo\tbar:1|c",  // non-printable or non-ascii is removed
		"foo bar:1|c",   // spaces changed to _
		"foo/bar:1|c",   // / changed to -
		"rem*ove1:1|c",  // * removed
		"<remove2>:1|c", // < > removed
		"[remove3]:1|c", // [ ] removed
		"{remove4}:1|c", // { } removed
	)
	s.CheckAllApprox(t, []testCase{
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

func TestForwardedKeyParsing(t *testing.T) {
	s := NewTestServer()
	s.s.conf.forwardingEnabled = true
	s.s.conf.forwarderEnabled = true
	s.Start()
	defer s.Close()

	s.SendGostMessages(t, "f|foo:1|c", "f|f|bar:1|c", "f||baz:1|c", "quf|ux:1|c")
	s.CheckAllApprox(t, []testCase{
		{"quf|ux.count", 1.0},
	})

	// Push the forwarded messages through.
	s.aggregateForwardingFlushChan <- s.when
	time.Sleep(10 * time.Millisecond)
	s.aggregateForwardedFlushChan <- s.when

	msg := s.WaitForMessage()
	approx(t, msg.Parsed["global.foo.count"].Value, 1.0)
	approx(t, msg.Parsed["global.f|bar.count"].Value, 1.0)
	approx(t, msg.Parsed["global.|baz.count"].Value, 1.0)
}

func TestNoForwarding(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "f|foo:1|c")
	s.CheckAllApprox(t, []testCase{
		{"f|foo.count", 1.0},
	})
}

func TestSampleRates(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "a:1|c|@0.1", "b:1|c|@1.0", "c:1|c|@3.0", "d:1|c|@0.0", "e:1|c|@-0.5")
	msg := s.WaitForMessage()
	approx(t, msg.Parsed["com.example.a.count"].Value, 10.0)
	approx(t, msg.Parsed["com.example.b.count"].Value, 1.0)
	for _, key := range []string{"c", "d", "e"} {
		equals(t, msg.Parsed["com.example."+key+".count"], nil)
	}
}

func TestMultilineMessage(t *testing.T) {
	s := NewStartedTestServer()
	defer s.Close()
	s.SendGostMessages(t, "foobar:3|c\nfoobar:5|c\nbaz:200|g")
	s.CheckAllApprox(t, []testCase{
		{"foobar.count", 8.0},
		{"foobar.rate", 4.0},
		{"baz.gauge", 200.0},
		{"gost.packets_received.count", 1.0},
		{"gost.packets_received.rate", 0.5},
	})
}

type graphiteValue struct {
	Value     float64
	Timestamp time.Time
}

type graphiteMessages struct {
	Raw    []string
	Parsed map[string]*graphiteValue
}

func parseRawGraphiteMessages(raw []string) *graphiteMessages {
	parsed := make(map[string]*graphiteValue)
	for _, line := range raw {
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
	return &graphiteMessages{raw, parsed}
}
