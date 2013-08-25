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
	"time"
)

const (
	incomingQueueSize = 100
	udpBufSize        = 10e3 // 10kB buffer, so we can handle huge messages
)

var (
	configFile = flag.String("conf", "conf.toml", "TOML configuration file")
	conf       *Conf

	namespace []string                              // determined from conf.Namespace
	incoming  = make(chan *Stat, incomingQueueSize) // incoming stats are passed to the aggregator
	outgoing  = make(chan []byte)                   // outgoing Graphite messages

	stats = NewBufferedCounts() // e.g. counters -> { foo.bar -> 2 }
	// sets and timers require additional structures for intermediate computations.
	setValues     map[string]map[float64]struct{}
	timerValues   map[string][]float64
	tagToStatType = map[string]StatType{
		"c":  StatCounter,
		"g":  StatGauge,
		"ms": StatTimer,
		"s":  StatSet,
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
	Type       StatType
	Name       []string
	Value      float64
	SampleRate float64
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
	GraphiteAddr    string       `toml:"graphite_addr"`
	Port            int          `toml:"port"`
	Debug           bool         `toml:"debug"`
	FlushIntervalMS int          `toml:"flush_interval_ms"`
	Namespace       string       `toml:"namespace"`
	OsStats         *OsStatsConf `toml:"os_stats"`
}

func handleMessages(messages []byte) {
	for _, msg := range bytes.Split(messages, []byte{'\n'}) {
		stat, ok := parseStatsdMessage(msg)
		if !ok {
			log.Println("bad message:", string(msg))
			metaCount("bad_messages_seen")
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
		metaCount("packets_received")
		if n >= udpBufSize {
			return fmt.Errorf("UDP message too large.")
		}
		messages := make([]byte, n)
		copy(messages, buf)
		go handleMessages(messages)
	}
}

// clearStats resets the state of all the stat types.
func clearStats() {
	stats.Clear()
	setValues = make(map[string]map[float64]struct{})
	timerValues = make(map[string][]float64)
}

// postProcessStats computes derived stats prior to flushing.
func postProcessStats() {
	// Compute the per-second rate for each counter
	rateFactor := float64(conf.FlushIntervalMS) / 1000
	for key, value := range stats.Get("counters") {
		stats.Set("counter_rates", key, value/rateFactor)
	}
	// Compute the size of each set
	for key, value := range setValues {
		stats.Set("sets", key, float64(len(value)))
	}

	// Process all the various stats for each timer
	for key, values := range timerValues {
		if len(values) == 0 {
			continue
		}
		timerStats := make(map[string]float64)
		count := float64(len(values))
		// count is the number of timer values recorded
		timerStats["count"] = count
		// count_rate is the rate (per second) at which timings were recorded
		timerStats["count_rate"] = count / rateFactor
		// sum is the total sum of all timings. You can use count and sum to compute statistics across buckets.
		sum := float64(0)
		for _, t := range values {
			sum += t
		}
		timerStats["sum"] = sum
		mean := sum / count
		timerStats["mean"] = mean
		sumSquares := float64(0)
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
			k := "timers." + statName
			stats.Set(k, key, value)
		}
	}
}

// createGraphiteMessage buffers up a graphite message. We could write directly to the connection and avoid
// the extra buffering but this allows us to use separate goroutines to write to graphite (potentially slow)
// and aggregate (happening all the time).
func createGraphiteMessage() (n int, msg []byte) {
	buf := &bytes.Buffer{}
	timestamp := time.Now().Unix()
	for typ, s := range stats {
		for key, value := range s {
			n++
			fmt.Fprintf(buf, "%s %f %d\n", strings.Join(append(namespace, typ, key), "."), value, timestamp)
		}
	}
	return n, buf.Bytes()
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
				stats.Inc("counters", key, stat.Value/stat.SampleRate)
			case StatSet:
				set, ok := setValues[key]
				if ok {
					set[stat.Value] = struct{}{}
				} else {
					setValues[key] = map[float64]struct{}{stat.Value: struct{}{}}
				}
			case StatGauge:
				stats.Set("gauges", key, stat.Value)
			case StatTimer:
				timerValues[key] = append(timerValues[key], stat.Value)
			}
		case <-ticker.C:
			postProcessStats()
			n, msg := createGraphiteMessage()
			if n > 0 {
				dbg.Printf("Flushing %d stats.\n", n)
				outgoing <- msg
			} else {
				dbg.Println("No stats to flush.")
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
	if conf.OsStats != nil {
		fmt.Println("checking os stats")
		go checkOsStats()
	}

	udpAddr := fmt.Sprintf("localhost:%d", conf.Port)
	udp, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Listening for UDP client requests on", udp)
	log.Fatal(clientServer(udp))
}
