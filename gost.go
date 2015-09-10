package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/cespare/gost/internal/llog"
)

const (
	incomingQueueSize = 100

	// Gost used a number of fixed-size buffers for incoming messages to limit allocations. This is controlled
	// by udpBufSize and nUDPBufs. Note that gost cannot accept statsd messages larger than udpBufSize.
	// In this case, the total size of buffers for incoming messages is 10e3 * 1000 = 10MB.
	udpBufSize = 10e3
	nUDPBufs   = 1000

	// All TCP connections managed by gost have this keepalive duration applied
	tcpKeepAlivePeriod = 30 * time.Second
)

var (
	configFile       = flag.String("conf", "conf.toml", "TOML configuration file")
	forwardKeyPrefix = []byte("f|")

	version = "no version " // can be overridden at build time with -ldflags -X
)

type Server struct {
	conf *Conf
	l    *llog.Logger
	quit chan struct{} // For shutting down everything

	bufPool chan []byte // pool of buffers for incoming messages

	metaStats chan *Stat

	incoming chan *Stat  // incoming stats are passed to the aggregator
	outgoing chan []byte // outgoing Graphite messages

	stats *BufferedStats

	forwardingStats    *BufferedStats // Counters to be forwarded
	forwardingIncoming chan *Stat     // Incoming messages to be forwarded
	forwardingOutgoing chan []byte    // Outgoing forwarded messages

	forwarderIncoming chan *BufferedStats // incoming forwarded messages
	forwardedStats    *BufferedStats

	debugServer *dServer

	// The flushTickers and now are functions that the tests can stub out.
	aggregateFlushTicker           func() <-chan time.Time
	aggregateForwardedFlushTicker  func() <-chan time.Time
	aggregateForwardingFlushTicker func() <-chan time.Time
	now                            func() time.Time

	// Used for any storage the platform-specific os stats checking needs.
	osData OSData
}

// NewServer sets up a new server with some configuration without starting goroutines or listeners. out is
// where logs are written.
func NewServer(conf *Conf, out io.Writer) *Server {
	// TODO: May want to make this configurable later.
	logger := llog.NewLogger(log.New(out, "", log.LstdFlags), conf.DebugLogging)
	s := &Server{
		conf:            conf,
		l:               logger,
		quit:            make(chan struct{}),
		bufPool:         make(chan []byte, nUDPBufs),
		metaStats:       make(chan *Stat),
		incoming:        make(chan *Stat, incomingQueueSize),
		outgoing:        make(chan []byte),
		stats:           NewBufferedStats(conf.FlushIntervalMS),
		forwardingStats: NewBufferedStats(conf.FlushIntervalMS),
		// Having forwardingIncoming be nil when forwarding is not enabled ensures that gost will crash fast if
		// somehow messages are interpreted as forwarded messages even when forwarding is turned off (which should
		// never happen). Otherwise the behavior would be to fill up the queue and then deadlock.
		forwardingIncoming: nil,
		forwardingOutgoing: make(chan []byte),
		forwarderIncoming:  make(chan *BufferedStats, incomingQueueSize),
		forwardedStats:     NewBufferedStats(conf.FlushIntervalMS),
		debugServer:        &dServer{l: logger},
		now:                time.Now,
	}
	s.InitOSData()
	// Preallocate the UDP buffer pool
	for i := 0; i < nUDPBufs; i++ {
		s.bufPool <- make([]byte, udpBufSize)
	}

	s.aggregateFlushTicker = func() <-chan time.Time {
		return time.NewTicker(time.Duration(s.conf.FlushIntervalMS) * time.Millisecond).C
	}
	s.aggregateForwardedFlushTicker = s.aggregateFlushTicker
	s.aggregateForwardingFlushTicker = s.aggregateFlushTicker

	return s
}

// Listen launches the various server goroutines and starts the various listeners.
// If the listener params are nil, these are constructed from the parameters in the conf. Otherwise they are
// used as-is. This makes it possible for the tests to construct listeners on an available port and pass them
// in.
func (s *Server) Listen(clientConn *net.UDPConn, forwardListener, debugListener net.Listener) error {
	go s.handleMetaStats()
	go s.flush()
	go s.aggregate()
	if s.conf.OSStats != nil {
		go s.checkOSStats()
	}
	if s.conf.Scripts != nil {
		go s.runScripts()
	}

	if s.conf.forwardingEnabled {
		s.forwardingIncoming = make(chan *Stat, incomingQueueSize)
		go s.flushForwarding()
		go s.aggregateForwarding()
	}

	errorCh := make(chan error)
	if s.conf.forwarderEnabled {
		if forwardListener == nil {
			l, err := net.Listen("tcp", s.conf.ForwarderListenAddr)
			if err != nil {
				return err
			}
			forwardListener = tcpKeepAliveListener{l.(*net.TCPListener)}
		}
		s.l.Println("Listening for forwarded gost messages on", forwardListener.Addr())
		go s.aggregateForwarded()
		go func() {
			errorCh <- s.forwardServer(forwardListener)
		}()
	}

	if err := s.debugServer.Start(s.conf.DebugPort, debugListener); err != nil {
		return err
	}

	if clientConn == nil {
		udpAddr := fmt.Sprintf("localhost:%d", s.conf.Port)
		udp, err := net.ResolveUDPAddr("udp", udpAddr)
		if err != nil {
			return err
		}
		clientConn, err = net.ListenUDP("udp", udp)
		if err != nil {
			return err
		}
	}
	s.l.Println("Listening for UDP client requests on", clientConn.LocalAddr())
	go func() {
		errorCh <- s.clientServer(clientConn)
	}()

	// Indicate that we've started
	s.metaInc("server_start")

	return <-errorCh
}

type StatType int

const (
	StatCounter StatType = iota
	StatGauge
	StatTimer
	StatSet
)

type Stat struct {
	Type       StatType
	Forward    bool
	Name       string
	Value      float64
	SampleRate float64 // Only for counters
}

// tagToStatType maps a tag (e.g., []byte("c")) to a StatType (e.g., StatCounter).
func tagToStatType(b []byte) (StatType, bool) {
	switch len(b) {
	case 1:
		switch b[0] {
		case 'c':
			return StatCounter, true
		case 'g':
			return StatGauge, true
		case 's':
			return StatSet, true
		}
	case 2:
		if b[0] == 'm' && b[1] == 's' {
			return StatTimer, true
		}
	}
	return 0, false
}

func (s *Server) handleMessages(buf []byte) {
	for _, msg := range bytes.Split(buf, []byte{'\n'}) {
		s.handleMessage(msg)
	}
	s.bufPool <- buf[:cap(buf)] // Reset buf's length and return to the pool
}

func (s *Server) handleMessage(msg []byte) {
	if len(msg) == 0 {
		return
	}
	s.debugServer.Print("[in] ", msg)
	stat, ok := parseStatsdMessage(msg, s.conf.forwardingEnabled)
	if !ok {
		s.l.Println("bad message:", string(msg))
		s.metaInc("errors.bad_message")
		return
	}
	if stat.Forward {
		if stat.Type != StatCounter {
			s.metaInc("errors.bad_metric_type_for_forwarding")
			return
		}
		s.forwardingIncoming <- stat
	} else {
		s.incoming <- stat
	}
}

func (s *Server) clientServer(c *net.UDPConn) error {
	for {
		buf := <-s.bufPool
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		s.metaInc("packets_received")
		if n >= udpBufSize {
			s.metaInc("errors.udp_message_too_large")
			continue
		}
		go s.handleMessages(buf[:n])
	}
}

// aggregateForwarded merges forwarded gost messages.
func (s *Server) aggregateForwarded() {
	ticker := s.aggregateForwardedFlushTicker()
	for {
		select {
		case count := <-s.forwarderIncoming:
			s.forwardedStats.Merge(count)
		case <-ticker:
			n, msg := s.forwardedStats.CreateGraphiteMessage(s.conf.ForwardedNamespace,
				"distinct_forwarded_metrics_flushed", s.now())
			s.l.Debugf("Sending %d forwarded stat(s) to graphite.", n)
			s.outgoing <- msg
			s.forwardedStats.Clear(!s.conf.ClearStatsBetweenFlushes)
		case <-s.quit:
			return
		}
	}
}

func (s *Server) handleForwarded(c net.Conn) {
	defer c.Close()
	for {
		var counts map[string]float64
		// Have to make a new decoder each time because the type info is sent over in each message.
		// TODO(caleb): make this more efficient by only creating an encoder and decoder once.
		if err := gob.NewDecoder(c).Decode(&counts); err != nil {
			if err == io.EOF {
				return
			}
			s.l.Println("Error reading forwarded message:", err)
			s.metaInc("errors.forwarded_message_read")
			return
		}
		s.metaInc("forwarded_messages")
		s.forwarderIncoming <- &BufferedStats{Counts: counts}
	}
}

func (s *Server) forwardServer(listener net.Listener) error {
	defer listener.Close()
	for {
		c, err := listener.Accept()
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Temporary() {
				delay := 10 * time.Millisecond
				s.l.Printf("Accept error: %v; retrying in %v", e, delay)
				time.Sleep(delay)
				continue
			}
			return err
		}
		go s.handleForwarded(c)
	}
}

// aggregateForwarding reads incoming forward messages and aggregates them. Every flush interval it forwards
// the collected stats.
func (s *Server) aggregateForwarding() {
	ticker := s.aggregateForwardingFlushTicker()
	for {
		select {
		case stat := <-s.forwardingIncoming:
			if stat.Type == StatCounter {
				s.forwardingStats.AddCount(stat.Name, stat.Value/stat.SampleRate)
			}
		case <-ticker:
			n, msg, err := s.forwardingStats.CreateForwardMessage()
			if err != nil {
				s.l.Debugln("Error: Could not serialize forwarded message:", err)
			}
			if n > 0 {
				s.l.Debugf("Forwarding %d stat(s).", n)
				s.forwardingOutgoing <- msg
			} else {
				s.l.Debugln("No stats to forward.")
			}
			// Always delete forwarded stats -- they are cleared/preserved between flushes at the receiving end.
			s.forwardingStats.Clear(false)
		case <-s.quit:
			return
		}
	}
}

// flushForwarding pushes forwarding messages to another gost instance.
func (s *Server) flushForwarding() {
	conn := DialPConn(s.conf.ForwardingAddr)
	defer conn.Close()
	for {
		select {
		case msg := <-s.forwardingOutgoing:
			debugMsg := fmt.Sprintf("<binary forwarding message; len = %d bytes>", len(msg))
			s.debugServer.Print("[forward]", []byte(debugMsg))
			start := time.Now()
			if _, err := conn.Write(msg); err != nil {
				s.metaInc("errors.forwarding_write")
				s.l.Printf("Warning: could not write forwarding message to %s: %s", s.conf.ForwardingAddr, err)
			}
			s.metaTimer("graphite_write", time.Since(start))
		case <-s.quit:
			return
		}
	}
}

// aggregate reads the incoming messages and aggregates them. It sends them to be flushed every flush
// interval.
func (s *Server) aggregate() {
	ticker := s.aggregateFlushTicker()
	for {
		select {
		case stat := <-s.incoming:
			key := stat.Name
			switch stat.Type {
			case StatCounter:
				s.stats.AddCount(key, stat.Value/stat.SampleRate)
			case StatSet:
				s.stats.AddSetItem(key, stat.Value)
			case StatGauge:
				s.stats.SetGauge(key, stat.Value)
			case StatTimer:
				s.stats.RecordTimer(key, stat.Value)
			}
		case <-ticker:
			n, msg := s.stats.CreateGraphiteMessage(s.conf.Namespace, "distinct_metrics_flushed", s.now())
			s.l.Debugf("Flushing %d stat(s).", n)
			s.outgoing <- msg
			s.stats.Clear(!s.conf.ClearStatsBetweenFlushes)
		case <-s.quit:
			return
		}
	}
}

// flush pushes outgoing messages to graphite.
func (s *Server) flush() {
	conn := DialPConn(s.conf.GraphiteAddr)
	defer conn.Close()
	for {
		select {
		case msg := <-s.outgoing:
			s.debugServer.Print("[out] ", msg)
			start := time.Now()
			if _, err := conn.Write(msg); err != nil {
				s.metaInc("errors.graphite_write")
				s.l.Printf("Warning: could not write message to Graphite at %s: %s", s.conf.GraphiteAddr, err)
			}
			s.metaTimer("graphite_write", time.Since(start))
		case <-s.quit:
			return
		}
	}
}

// dServer listens on a local tcp port and prints out debugging info to clients that connect.
type dServer struct {
	l *llog.Logger
	sync.Mutex
	Clients []net.Conn
}

// If listener is non-nil, then it's used; otherwise listen on TCP using the given port.
func (s *dServer) Start(port int, listener net.Listener) error {
	if listener == nil {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		s.l.Println("Listening for debug TCP clients on", addr)
		var err error
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				continue
			}
			s.Lock()
			s.Clients = append(s.Clients, c)
			s.l.Debugf("Debug client connected. Currently %d connected client(s).", len(s.Clients))
			s.Unlock()
		}
	}()
	return nil
}

func (s *dServer) closeClient(client net.Conn) {
	for i, c := range s.Clients {
		if c == client {
			s.Clients = append(s.Clients[:i], s.Clients[i+1:]...)
			client.Close()
			s.l.Debugf("Debug client disconnected. Currently %d connected client(s).", len(s.Clients))
			return
		}
	}
}

func (s *dServer) Print(tag string, msg []byte) {
	s.Lock()
	defer s.Unlock()
	if len(s.Clients) == 0 {
		return
	}

	closed := []net.Conn{}
	for _, line := range bytes.Split(msg, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		msg := append([]byte(tag), line...)
		msg = append(msg, '\n')
		for _, c := range s.Clients {
			// Set an aggressive write timeout so a slow debug client can't impact performance.
			c.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
			if _, err := c.Write(msg); err != nil {
				closed = append(closed, c)
				continue
			}
		}
		for _, c := range closed {
			s.closeClient(c)
		}
	}
}

type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (l tcpKeepAliveListener) Accept() (net.Conn, error) {
	c, err := l.AcceptTCP()
	if err != nil {
		return nil, err
	}
	if err := c.SetKeepAlive(true); err != nil {
		return nil, err
	}
	if err := c.SetKeepAlivePeriod(tcpKeepAlivePeriod); err != nil {
		return nil, err
	}
	return c, nil
}

func main() {
	versionFlag := flag.Bool("version", false, "Display the version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}

	conf, err := parseConf()
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(NewServer(conf, os.Stdout).Listen(nil, nil, nil))
}
