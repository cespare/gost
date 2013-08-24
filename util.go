package main

import (
	"bytes"
	"log"
	"strings"
	"regexp"
	"strconv"
)

var (
	sanitizeSpaces = regexp.MustCompile(`\s+`)
	sanitizeSlashes = strings.NewReplacer("/", "-")
	sanitizeDisallowed = regexp.MustCompile(`[^a-zA-Z_\-0-9\.]`)
)

func sanitizeKey(key string) string {
	key = sanitizeSpaces.ReplaceAllLiteralString(key, "_")
	key = sanitizeSlashes.Replace(key)
	return sanitizeDisallowed.ReplaceAllLiteralString(key, "")
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
	nameParts := strings.Split(string(parts[0]), ".")
	stat.Name = make([]string, len(nameParts))
	for i, part := range nameParts {
		stat.Name[i] = sanitizeKey(part)
	}
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
			// Statsd ignores sample rates > 0, but I'm going to be more strict.
			if sampleRate > 1.0 || sampleRate < 0 {
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

