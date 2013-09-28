package main

import (
	"bytes"
	"log"
	"strconv"
)

var dbg _dbg

type _dbg struct{}

func (d _dbg) Printf(format string, args ...interface{}) {
	if conf.DebugLogging {
		log.Printf(format, args...)
	}
}

func (d _dbg) Println(args ...interface{}) {
	if conf.DebugLogging {
		log.Println(args...)
	}
}

// metaCount increments a counter for an internal gost stat.
func metaCount(name string) {
	s := &Stat{
		Type:       StatCounter,
		Name:       "gost." + name,
		Value:      1.0,
		SampleRate: 1.0,
	}
	incoming <- s
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// sanitize key does several things:
// - collapse consecutive spaces into single _
// - Replace / with -
// - Remove disallowed characters (< and >)
// This used to be done with a combination of strings.Replacer, regular expressions, and strings.Map, but was
// rewritten to do a single pass for efficiency.
func sanitizeKey(key []byte) string {
	inSpace := false
	var buf bytes.Buffer
	for _, c := range key {
		if inSpace {
			if isSpace(c) {
				continue // Still in a space group
			}
			buf.WriteByte('_')
			inSpace = false
		}
		if isSpace(c) {
			inSpace = true
			continue
		}

		switch c {
		case '/':
			buf.WriteByte('-')
		case '<', '>': // disallowed
		default:
			buf.WriteByte(c)
		}
	}
	return buf.String()
}

func parseStatsdMessage(msg []byte) (stat *Stat, ok bool) {
	stat = &Stat{}
	parts := bytes.Split(bytes.TrimSpace(msg), []byte{':'})
	if len(parts) == 0 || len(parts[0]) == 0 {
		return nil, false
	}
	// NOTE: It looks like statsd will accept multiple values for a key at once (e.g., foo.bar:1|c:2.5|g), but
	// this isn't actually documented and I'm not going to support it for now.
	if len(parts) != 2 {
		return nil, false
	}
	stat.Name = sanitizeKey(parts[0])
	fields := bytes.Split(parts[1], []byte{'|'})
	if len(fields) < 2 || len(fields) > 3 {
		return nil, false
	}
	value, err := strconv.ParseFloat(string(fields[0]), 64)
	if err != nil {
		return nil, false
	}
	stat.Value = value

	metricType := string(bytes.TrimSpace(fields[1]))
	typ, ok := tagToStatType[metricType]
	if !ok {
		return nil, false
	}
	stat.Type = typ
	switch stat.Type {
	case StatSet, StatGauge:
		return stat, true
	}

	sampleRate := 1.0
	if len(fields) == 3 {
		sampleRateBytes := fields[2]
		if len(sampleRateBytes) < 2 || sampleRateBytes[0] != '@' {
			return nil, false
		}
		var err error
		sampleRate, err = strconv.ParseFloat(string(sampleRateBytes[1:]), 64)
		if err != nil {
			return nil, false
		}
		// Statsd ignores sample rates > 0, but I'm going to be more strict.
		if sampleRate > 1.0 || sampleRate <= 0 {
			return nil, false
		}
	}
	stat.SampleRate = sampleRate
	return stat, true
}

type BufferedCounts map[string]map[string]float64

func NewBufferedCounts() BufferedCounts {
	return make(BufferedCounts)
}

func (c BufferedCounts) Get(key string) map[string]float64 { return c[key] }

func (c BufferedCounts) Set(key1, key2 string, value float64) {
	s, ok := c[key1]
	if ok {
		s[key2] = value
	} else {
		c[key1] = map[string]float64{key2: value}
	}
}

func (c BufferedCounts) Inc(key1, key2 string, delta float64) {
	s, ok := c[key1]
	if ok {
		s[key2] += delta
	} else {
		c[key1] = map[string]float64{key2: delta}
	}
}
