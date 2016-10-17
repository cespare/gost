package proc

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

var memInfoParseErr = errors.New("Cannot parse /proc/meminfo")

// MemInfo returns the memory information fields from /proc/meminfo, such as "MemTotal" and "MemFree". Byte
// values are returned in bytes, not kB.
func MemInfo() (map[string]uint64, error) {
	result := make(map[string]uint64)
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) != 2 {
			return nil, memInfoParseErr
		}
		key := parts[0]
		valString := strings.TrimSpace(parts[1])
		multiplier := uint64(1)
		if strings.HasSuffix(valString, " kB") {
			multiplier = 1000
			valString = strings.TrimSuffix(valString, " kB")
		}
		val, err := strconv.ParseUint(valString, 10, 64)
		if err != nil {
			return nil, err
		}
		val *= multiplier
		result[key] = val
	}
	return result, nil
}
