package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"
)

const (
	// Must be aligned with statsd's flush interval
	interval    = 1000 * time.Second
	testLength  = 500 * time.Second
	parallelism = 10
	startDelay  = 30 * time.Microsecond

	listenaddr = "localhost:2003"
	statsdaddr = "localhost:8125"
)

var (
	reportedTotals = make(chan float64, 20)
	key            = []byte("statsd_perf")
	//reportedKey    = append(key, []byte(".count")...) // gost
	reportedKey = append([]byte("counts."), key...) // statsd
	udp         *net.UDPAddr
	msg         = []byte(fmt.Sprintf("%s:1|c", key))
)

func init() {
	var err error
	udp, err = net.ResolveUDPAddr("udp", statsdaddr)
	if err != nil {
		log.Fatal(err)
	}
}

func perfWorker(delay time.Duration, counts chan float64) {
	p, err := net.ListenPacket("udp", ":0")
	if err != nil {
		log.Fatal(err)
	}
	c := p.(*net.UDPConn)

	start := time.Now()
	var total float64
	for {
		if time.Since(start) >= testLength {
			counts <- total
			return
		}
		total++
		c.WriteToUDP(msg, udp)
		time.Sleep(delay)
	}
}

func runTest(delay time.Duration) {
	counts := make(chan float64)
	for i := 0; i < parallelism; i++ {
		go perfWorker(delay, counts)
	}
	var total float64
	for i := 0; i < parallelism; i++ {
		total += <-counts
	}
	reportedTotal := <-reportedTotals
	qps := total / float64(testLength.Seconds())
	fmt.Printf("qps = %.1f, loss rate = %.5f%%\n", qps, 100*(1.0-(reportedTotal/total)))
}

func handle(c net.Conn) {
	scanner := bufio.NewScanner(c)
	for scanner.Scan() {
		line := scanner.Bytes()
		fields := bytes.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if bytes.HasSuffix(fields[0], reportedKey) {
			n, err := strconv.ParseFloat(string(fields[1]), 64)
			if err != nil {
				continue
			}
			reportedTotals <- n
		}
	}
}

func main() {
	l, err := net.Listen("tcp", listenaddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go handle(c)
		}
	}()

	delay := startDelay
	go runTest(delay)
	for _ = range time.NewTicker(interval).C {
		delay /= 2
		go runTest(delay)
	}
}
