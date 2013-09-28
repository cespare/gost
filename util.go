package main

import (
	"bytes"
	"log"
	"strconv"
	"unsafe"
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

// parseKey does several things:
// - collapse consecutive spaces into single _
// - Replace / with -
// - Remove disallowed characters (< and >)
// - Stops on : -- this indicates the end of a key
// Sanitized is the sanitized key part (before the :), ok indicates whether this function successfully found a
// : to split on, and rest is the remainder of the input after the :.
//

// rewritten to do a single pass for efficiency.
func parseKey(key []byte) (sanitized string, ok bool, rest []byte) {
	inSpace := false
	var buf bytes.Buffer
	for i, c := range key {
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
		case ':':
			return buf.String(), true, key[i+1:]
		default:
			buf.WriteByte(c)
		}
	}
	return "", false, nil
}

// TODO XXX HACK FIXME
// parseFloat reads a float64 from b. This uses unsafe hackery courtesy of
// https://code.google.com/p/go/issues/detail?id=2632#c16 -- if that issue gets fixed, we should switch to
// doing that instead of using an unsafe []byte -> string conversion.
func parseFloat(b []byte) (float64, error) {
	s := *(*string)(unsafe.Pointer(&b))
	return strconv.ParseFloat(s, 64)
}

// parseValue reads a float64 value off of b, expecting it to be followed by a | character.
func parseValue(b []byte) (f float64, ok bool, rest []byte) {
	endingPipe := false
	var i int
	var c byte
	for i, c = range b {
		if c == '|' {
			endingPipe = true
			break
		}
	}
	if !endingPipe {
		return 0, false, nil
	}
	f, err := parseFloat(b[:i])
	if err != nil {
		return 0, false, nil
	}
	return f, true, b[i+1:]
}

func parseMetricType(b []byte) (typ StatType, ok bool, rest []byte) {
	tag := b
	rest = nil
	for i, c := range b {
		if c == '|' {
			tag = b[:i]
			rest = b[i+1:]
			break
		}
	}

	typ, ok = tagToStatType[string(tag)]
	if !ok {
		return 0, false, nil
	}
	return typ, true, rest
}

func parseRate(b []byte) (float64, bool) {
	if len(b) < 2 {
		return 0, false
	}
	if b[0] != '@' {
		return 0, false
	}
	f, err := parseFloat(b[1:])
	if err != nil {
		return 0, false
	}
	return f, true
}

func parseStatsdMessage(msg []byte) (stat *Stat, ok bool) {
	stat = &Stat{}
	name, ok, rest := parseKey(msg)
	if !ok {
		return nil, false
	}
	if name == "" { // empty name is invalid
		return nil, false
	}
	stat.Name = name

	// NOTE: It looks like statsd will accept multiple values for a key at once (e.g., foo.bar:1|c:2.5|g), but
	// this isn't actually documented and I'm not going to support it for now.
	stat.Value, ok, rest = parseValue(rest)
	if !ok {
		return nil, false
	}
	stat.Type, ok, rest = parseMetricType(rest)
	if !ok {
		return nil, false
	}

	switch stat.Type {
	case StatSet, StatGauge:
		if len(rest) > 0 {
			return nil, false
		}
		return stat, true
	}

	rate := 1.0
	if len(rest) > 0 {
		rate, ok = parseRate(rest)
		if !ok {
			return nil, false
		}
		// Statsd ignores sample rates > 0, but I'm going to be more strict.
		if rate > 1.0 || rate <= 0 {
			return nil, false
		}
	}
	stat.SampleRate = rate
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
