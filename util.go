package main

import (
	"bytes"
	"log"
	"regexp"
	"strconv"
	"strings"
)

var (
	sanitizeSpaces     = regexp.MustCompile(`\s+`)
	sanitizeSlashes    = strings.NewReplacer("/", "-")
	sanitizeDisallowed = regexp.MustCompile(`[^a-zA-Z_\-0-9\.]`)
	dbg                _dbg
)

type _dbg struct{}

func (d _dbg) Printf(format string, args ...interface{}) {
	if conf.Debug {
		log.Printf("DEBUG: "+format, args...)
	}
}

func (d _dbg) Println(args ...interface{}) {
	if conf.Debug {
		args = append([]interface{}{"DEBUG:"}, args...)
		log.Println(args...)
	}
}

// metaCount increments a counter for an internal gost stat.
func metaCount(name string) {
	s := &Stat{
		Type:       StatCounter,
		Name:       []string{"gost", name},
		Value:      float64(1),
		SampleRate: float64(1),
	}
	incoming <- s
}

func sanitizeKey(key string) string {
	key = sanitizeSpaces.ReplaceAllLiteralString(key, "_")
	key = sanitizeSlashes.Replace(key)
	return sanitizeDisallowed.ReplaceAllLiteralString(key, "")
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
	nameParts := strings.Split(string(parts[0]), ".")
	stat.Name = make([]string, len(nameParts))
	for i, part := range nameParts {
		stat.Name[i] = sanitizeKey(part)
	}

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

	sampleRate := float64(1)
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

func (c BufferedCounts) Clear() {
	for k := range c {
		delete(c, k)
	}
}
