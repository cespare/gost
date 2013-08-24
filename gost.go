package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

const udpBufSize = 10e3 // 10kB buffer, so we can handle huge messages

var (
	configFile = flag.String("conf", "conf.toml", "TOML configuration file")
	conf       *Conf

	namespace []string            // determined from conf.Namespace
	incoming  = make(chan Stat)   // incoming stats are passed to the aggregator
	outgoing  = make(chan []byte) // outgoing Graphite messages

	stats = make(map[string]map[string]float64) // e.g. counters -> { foo.bar -> 2 }
	statTypes     = []string{
		"counters",
		"counter_rates",
	}
)

type StatType int

const (
	StatCounter StatType = iota
	StatGauge
	StatTimer
	StatSet
)

type Stat struct {
	Type  StatType
	Name  []string
	Value float64
}

type Conf struct {
	GraphiteAddr    string `toml:"graphite_addr"`
	Port            int    `toml:"port"`
	Debug           bool   `toml:"debug"`
	MgmtPort        int    `toml:"mgmt_port"`
	FlushIntervalMS int    `toml:"flush_interval_ms"`
	Namespace       string `toml:"namespace"`
}

func parseStatsdMessage(msg []byte) (stat Stat, ok bool) {
	stat = Stat{}
	parts := bytes.Split(bytes.TrimSpace(msg), []byte{':'})
	if len(parts) == 0 || len(parts[0]) == 0 {
		return stat, false
	}
	// NOTE: It looks like statsd will accept multiple values for a key at once (e.g., foo.bar:1|c:2.5|g), but
	// this isn't actually documented and I'm not going to support it for now.
	if len(parts) > 2 {
		return stat, false
	}
	// TODO: sanitize key
	stat.Name = strings.Split(string(parts[0]), ".")
	stat.Value = float64(1)
	if len(parts) > 1 {
		sampleRate := float64(1)
		fields := bytes.Split(parts[1], []byte{'|'})
		if len(fields) < 2 || len(fields) > 3 {
			return stat, false
		}
		if len(fields) == 3 {
			sampleRateBytes := fields[2]
			if len(sampleRateBytes) < 2 || sampleRateBytes[0] != '@' {
				// TODO: log bad message
				return stat, false
			}
			var err error
			sampleRate, err = strconv.ParseFloat(string(sampleRateBytes[1:]), 64)
			if err != nil {
				// TODO: log bad message
				return stat, false
			}
		}
		metricType := string(bytes.TrimSpace(fields[1]))
		switch metricType {
		case "ms":
			log.Println("timers are not handled yet.")
			return stat, false
		case "g":
			log.Println("gauges are not handled yet.")
			return stat, false
		case "s":
			log.Println("sets are not handled yet.")
			return stat, false
		case "c":
			stat.Type = StatCounter
			n, err := strconv.ParseFloat(string(fields[0]), 64)
			if err != nil {
				// TODO: log bad message
				return stat, false
			}
			stat.Value = n / sampleRate
		default:
			// NOTE: stats treats unknown stats types as counters; I prefer to reject them.
			// TODO: log bad message
			return stat, false
		}
	}
	return stat, true
}

func handleMessages(messages []byte) {
	for _, msg := range bytes.Split(messages, []byte{'\n'}) {
		stat, ok := parseStatsdMessage(msg)
		if !ok {
			// TODO: log bad message
			return
		}
		incoming <- stat
	}
}

func clientServer(addr *net.UDPAddr) error {
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	buf := make([]byte, udpBufSize)
	for {
		n, _, err := c.ReadFromUDP(buf)
		// TODO: Should we try to recover from such errors?
		if err != nil {
			return err
		}
		if n >= udpBufSize {
			return fmt.Errorf("UDP message too large.")
		}
		messages := make([]byte, n)
		copy(messages, buf)
		go handleMessages(messages)
	}
}

// clearStats resets the state of all the various stat types.
func clearStats() {
	for _, typ := range statTypes {
		stats[typ] = make(map[string]float64)
	}
}

// postProcessStats computes derived stats prior to flushing.
func postProcessStats() {
	// Compute the per-second rate for each counter
	rateFactor := float64(conf.FlushIntervalMS) / 1000
	for key, value := range stats["counters"] {
		stats["counter_rates"][key] = value / rateFactor
	}
}

// createGraphiteMessage buffers up a graphite message. We could write directly to the connection and avoid
// the extra buffering but this allows us to use separate goroutines to write to graphite (potentially slow)
// and aggregate (happening all the time).
func createGraphiteMessage() []byte {
	buf := &bytes.Buffer{}
	timestamp := time.Now().Unix()
	for typ, s := range stats {
		for key, value := range s {
			fmt.Fprintf(buf, "%s %f %d\n", strings.Join(append(namespace, typ, key), "."), value, timestamp)
		}
	}
	return buf.Bytes()
}

// aggregate reads the incoming messages and aggregates them. It sends them to be flushed every flush
// interval.
func aggregate() {
	ticker := time.NewTicker(time.Duration(conf.FlushIntervalMS) * time.Millisecond)
	for {
		select {
		case stat := <-incoming:
			key := strings.Join(stat.Name, ".")
			switch stat.Type {
			case StatCounter:
				stats["counters"][key] += stat.Value
			}
		case <-ticker.C:
			postProcessStats()
			msg := createGraphiteMessage()
			if len(msg) > 0 {
				outgoing <- msg
			}
			clearStats()
		}
	}
}

// flush pushes outgoing messages to graphite.
func flush() {
	for msg := range outgoing {
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

func main() {
	parseConf()
	clearStats()
	go flush()
	go aggregate()

	udpAddr := fmt.Sprintf("localhost:%d", conf.Port)
	udp, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Listening for UDP client requests on", udp)
	log.Fatal(clientServer(udp))
}
