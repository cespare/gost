package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	incomingQueueSize = 100

	// Gost used a number of fixed-size buffers for incoming messages to limit allocations. This is controlled
	// by udpBufSize and nUDPBufs. Note that gost cannot accept statsd messages larger than udpBufSize.
	// In this case, the total size of buffers for incoming messages is 10e3 * 1000 = 10MB.
	udpBufSize = 10e3
	nUDPBufs   = 1000
)

var (
	configFile = flag.String("conf", "conf.toml", "TOML configuration file")
	conf       *Conf

	bufPool = make(chan []byte, nUDPBufs) // pool of buffers for incoming messagse

	namespace string                                // determined from conf.Namespace
	incoming  = make(chan *Stat, incomingQueueSize) // incoming stats are passed to the aggregator
	outgoing  = make(chan []byte)                   // outgoing Graphite messages

	stats = NewBufferedCounts() // e.g. counters -> { foo.bar -> 2 }
	// sets and timers require additional structures for intermediate computations.
	setValues   = make(map[string]map[float64]struct{})
	timerValues = make(map[string][]float64)

	forwardingEnabled bool                  // Whether configured to forward to another gost
	forwarderEnabled  bool                  // Whether configured to receive forwarded messages
	forwardStats      = NewBufferedCounts() // Forwarded counters and gauges
	forwardKeyPrefix  = []byte("f|")

	debugServer = &dServer{}

	// flushTicker and now are functions that the tests can stub out.
	flushTicker func() <-chan time.Time
	now         func() time.Time = time.Now
)

func init() {
	// Preallocate the UDP buffer pool
	for i := 0; i < nUDPBufs; i++ {
		bufPool <- make([]byte, udpBufSize)
	}
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
	Forwarded  bool
	Name       string
	Value      float64
	SampleRate float64
}

// tagToStatType maps a tag (e.g., []byte("c")) to a StatType (e.g., StatCounter).
// NOTE: This used to be a map[string]StatType but was changed for performance reasons.
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

type DiskUsageConf struct {
	Path   string `toml:"path"`
	Values string `toml:"values"`
}

type OsStatsConf struct {
	CheckIntervalMS int                       `toml:"check_interval_ms"`
	LoadAvg         []int                     `toml:"load_avg"`
	LoadAvgPerCPU   []int                     `toml:"load_avg_per_cpu"`
	DiskUsage       map[string]*DiskUsageConf `toml:"disk_usage"`
}

type Conf struct {
	GraphiteAddr             string       `toml:"graphite_addr"`
	ForwardingAddr           string       `toml:"forwarding_addr"`
	ForwarderListenAddr      string       `toml:"forwarder_listen_addr"`
	Port                     int          `toml:"port"`
	DebugPort                int          `toml:"debug_port"`
	DebugLogging             bool         `toml:"debug_logging"`
	ClearStatsBetweenFlushes bool         `toml:"clear_stats_between_flushes"`
	FlushIntervalMS          int          `toml:"flush_interval_ms"`
	Namespace                string       `toml:"namespace"`
	OsStats                  *OsStatsConf `toml:"os_stats"`
}

func handleMessages(buf []byte) {
	for _, msg := range bytes.Split(buf, []byte{'\n'}) {
		if len(msg) == 0 {
			continue
		}
		debugServer.Print("[in] ", msg)
		stat, ok := parseStatsdMessage(msg)
		if !ok {
			log.Println("bad message:", string(msg))
			metaCount("bad_messages_seen")
			continue
		}
		incoming <- stat
	}
	bufPool <- buf[:cap(buf)] // Reset buf's length and return to the pool
}

func clientServer(c *net.UDPConn) error {
	for {
		buf := <-bufPool
		n, _, err := c.ReadFromUDP(buf)
		// TODO: Should we try to recover from such errors?
		if err != nil {
			return err
		}
		metaCount("packets_received")
		if n >= udpBufSize {
			metaCount("udp_message_too_large")
			continue
		}
		go handleMessages(buf[:n])
	}
}

// clearStats resets the state of all the stat types.
func clearStats() {
	// There aren't great semantics for persisting timer values, so we clear them regardless.
	timerValues = make(map[string][]float64)

	if conf.ClearStatsBetweenFlushes {
		for name := range stats {
			delete(stats, name)
		}
		setValues = make(map[string]map[float64]struct{})
	} else {
		for name, s := range stats {
			if strings.HasPrefix(name, "timer.") {
				delete(stats, name)
			} else if name != "gauge" {
				for k := range s {
					s[k] = 0
				}
			}
		}
		for k := range setValues {
			setValues[k] = make(map[float64]struct{})
		}
	}

	if forwardingEnabled {
		// Always delete forwarded stats -- they are cleared/preserved between flushes at the receiving end.
		for name := range forwardStats {
			delete(forwardStats, name)
		}
	}
}

// postProcessStats computes derived stats prior to flushing.
func postProcessStats() {
	// Compute the per-second rate for each counter
	rateFactor := float64(conf.FlushIntervalMS) / 1000
	for key, value := range stats.Get("count") {
		stats.Set("rate", key, value/rateFactor)
	}
	// Compute the size of each set
	for key, value := range setValues {
		stats.Set("set", key, float64(len(value)))
	}

	// Process all the various stats for each timer
	for key, values := range timerValues {
		if len(values) == 0 {
			continue
		}
		timerStats := make(map[string]float64)
		count := float64(len(values))
		// rate is the rate (per second) at which timings were recorded (scaled for the sampling rate).
		timerStats["rate"] = stats.Get("timer.count")[key] / rateFactor
		// sum is the total sum of all timings. You can use count and sum to compute statistics across buckets.
		sum := 0.0
		for _, t := range values {
			sum += t
		}
		timerStats["sum"] = sum
		mean := sum / count
		timerStats["mean"] = mean
		sumSquares := 0.0
		for _, v := range values {
			d := v - mean
			sumSquares += d * d
		}
		timerStats["stdev"] = math.Sqrt(sumSquares / count)
		sort.Float64s(values)
		timerStats["min"] = values[0]
		timerStats["max"] = values[len(values)-1]
		if len(values)%2 == 0 {
			timerStats["median"] = float64(values[len(values)/2-1]+values[len(values)/2]) / 2
		} else {
			timerStats["median"] = float64(values[len(values)/2])
		}
		// Now write out all these stats as namespaced keys
		for statName, value := range timerStats {
			k := "timer." + statName
			stats.Set(k, key, value)
		}
	}
}

// createGraphiteMessage buffers up a graphite message. We could write directly to the connection and avoid
// the extra buffering but this allows us to use separate goroutines to write to graphite (potentially slow)
// and aggregate (happening all the time).
func createGraphiteMessage() (n int, msg []byte) {
	buf := &bytes.Buffer{}
	timestamp := now().Unix()
	for typ, s := range stats {
		for key, value := range s {
			n++
			fullKey := namespace + "." + key + "." + typ
			fmt.Fprintf(buf, "%s %f %d\n", fullKey, value, timestamp)
		}
	}
	return n, buf.Bytes()
}

// aggregate reads the incoming messages and aggregates them. It sends them to be flushed every flush
// interval.
func aggregate() {
	ticker := flushTicker()
	for {
		select {
		case stat := <-incoming:
			key := stat.Name
			switch stat.Type {
			case StatCounter:
				stats.Inc("count", key, stat.Value/stat.SampleRate)
			case StatSet:
				set, ok := setValues[key]
				if ok {
					set[stat.Value] = struct{}{}
				} else {
					setValues[key] = map[float64]struct{}{stat.Value: {}}
				}
			case StatGauge:
				stats.Set("gauge", key, stat.Value)
			case StatTimer:
				stats.Inc("timer.count", key, 1.0/stat.SampleRate)
				timerValues[key] = append(timerValues[key], stat.Value)
			}
		case <-ticker:
			postProcessStats()
			n, msg := createGraphiteMessage()
			if n > 0 {
				dbg.Printf("Flushing %d stats.\n", n)
				outgoing <- msg
			} else {
				dbg.Println("No stats to flush.")
			}
			clearStats()
			// In its own goroutine to avoid deadlock. If the incoming queue is full, this will hang.
			go metaGauge("distinct_metrics_flushed", float64(n))
		}
	}
}

// flush pushes outgoing messages to graphite.
func flush() {
	for msg := range outgoing {
		debugServer.Print("[out] ", msg)
		conn, err := net.Dial("tcp", conf.GraphiteAddr)
		if err != nil {
			log.Printf("Error: cannot connect to graphite at %s: %s\n", conf.GraphiteAddr, err)
			continue
		}
		if _, err := conn.Write(msg); err != nil {
			log.Println("Warning: could not write Graphite message.")
		}
		conn.Close()
	}
}

// dServer listens on a local tcp port and prints out debugging info to clients that connect.
type dServer struct {
	sync.Mutex
	Clients []net.Conn
}

func (s *dServer) Start(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Println("Listening for debug TCP clients on", addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				continue
			}
			s.Lock()
			s.Clients = append(s.Clients, c)
			dbg.Printf("Debug client connected. Currently %d connected client(s).", len(s.Clients))
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
			dbg.Printf("Debug client disconnected. Currently %d connected client(s).", len(s.Clients))
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

func main() {
	flag.Parse()
	parseConf()
	flushTicker = func() <-chan time.Time {
		return time.NewTicker(time.Duration(conf.FlushIntervalMS) * time.Millisecond).C
	}

	clearStats()
	go flush()
	go aggregate()
	if conf.OsStats != nil {
		go checkOsStats()
	}

	if err := debugServer.Start(conf.DebugPort); err != nil {
		log.Fatal(err)
	}

	udpAddr := fmt.Sprintf("localhost:%d", conf.Port)
	udp, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Listening for UDP client requests on", udp)
	conn, err := net.ListenUDP("udp", udp)
	if err != nil {
		log.Fatal(err)
	}
	go log.Fatal(clientServer(conn))

	t := time.NewTimer(15 * time.Second)
	<-t.C
}
