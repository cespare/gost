package main

import (
	"bytes"
	"reflect"
	"strconv"
	"unsafe"
)

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// parseKey does key sanitization (see Key Format in the readme) and stops on ':', which indicates the end of
// the key. key is the sanitized key part (before the ':'), ok indicates whether this function successfully
// found a ':' to split on, forward indicates whether this key is to be forwarded (forwarded keys start with
// forwardKeyPrefix and that prefix is stripped from key), and rest is the remainder of the input after the
// ':'.
func parseKey(b []byte, forwardingEnabled bool) (key string, ok bool, forward bool, rest []byte) {
	var buf bytes.Buffer
	forward = forwardingEnabled
	for i, c := range b {
		if forward && i < len(forwardKeyPrefix) {
			forward = (c == forwardKeyPrefix[i])
			if forward && i == len(forwardKeyPrefix)-1 {
				// We're forwarding this key; strip the prefix
				buf.Reset()
				continue
			}
		}
		if c < ' ' || c > '~' { // Remove any byte that isn't a printable ascii char
			continue
		}
		switch c {
		case ' ': // Replace space with _
			c = '_'
		case '/': // Replace / with -
			c = '-'
		case '<', '>', '*', '[', ']', '{', '}': // Remove <, >, *, [, ], {, and }
			continue
		case ':': // End of key
			return buf.String(), true, forward, b[i+1:]
		}
		buf.WriteByte(c)
	}
	return "", false, false, nil
}

// TODO XXX HACK FIXME
// parseFloat reads a float64 from b. This uses unsafe hackery courtesy of
// https://code.google.com/p/go/issues/detail?id=2632#c16 -- if that issue gets fixed, we should switch to
// doing that instead of using an unsafe []byte -> string conversion.
func parseFloat(b []byte) (float64, error) {
	var s string
	sh := (*reflect.StringHeader)(unsafe.Pointer(&s))
	sh.Data = uintptr(unsafe.Pointer(&b[0]))
	sh.Len = len(b)
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

	typ, ok = tagToStatType(tag)
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

func parseStatsdMessage(msg []byte, forwardingEnabled bool) (stat *Stat, ok bool) {
	stat = &Stat{}
	name, ok, forward, rest := parseKey(msg, forwardingEnabled)
	if !ok || name == "" { // empty name is invalid
		return nil, false
	}
	stat.Forward = forward
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
