package proc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var procNetProtoFiles = []string{"netstat", "snmp"}

// NetProtoStats parses the files /proc/net/netstat, and /proc/net/snmp.
// It returns the table of values as a 2-dimensional array from category -> key -> value. For example:
//
//     {
//       "Tcp": {
//         "ActiveOpens": 11023,
//         "PassiveOpens": 64,
//         ...
//       },
//      "Udp": {...},
//      ...
//     }
func NetProtoStats() (map[string]map[string]int64, error) {
	result := make(map[string]map[string]int64)
	for _, filename := range procNetProtoFiles {
		f, err := os.Open("/proc/net/" + filename)
		if err != nil {
			continue
		}
		defer f.Close()
		reader := bufio.NewReader(f)
		keys := []string{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					if len(keys) > 0 {
						return nil, fmt.Errorf("Read a header line without a following data line.")
					}
					break
				}
				return nil, err
			}
			if len(keys) == 0 {
				keys = strings.Fields(line)
			} else {
				values := strings.Fields(line)
				if len(values) != len(keys) {
					return nil, fmt.Errorf("Found a value line of a different length than the header line")
				}
				if keys[0] != values[0] || !strings.HasSuffix(keys[0], ":") {
					return nil, fmt.Errorf("Header or value lines don't match or don't start with a label")
				}
				label := keys[0][:len(keys[0])-1]
				lineMap := make(map[string]int64)
				for i, key := range keys {
					if i == 0 {
						continue
					}
					v, err := strconv.ParseInt(values[i], 10, 64)
					if err != nil {
						return nil, fmt.Errorf("Could not parse value.")
					}
					lineMap[key] = v
				}
				result[label] = lineMap
				keys = nil
			}
		}

		f.Close() // Double close is fine
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("None of the required /proc/net/ files could be found.")
	}
	return result, nil
}

var (
	netDevHeader = regexp.MustCompile(`^Inter-|\s*Receive\s*|\s*Transmit\s*$`)
	netDevErr    = errors.New("Error parsing /proc/net/dev")
)

// NetDevStats returns the data in /proc/net/dev.
// Example:
// eth0 -> bytes -> 12345
func NetDevStats() (receive, transmit map[string]map[string]uint64, err error) {
	receive = make(map[string]map[string]uint64)
	transmit = make(map[string]map[string]uint64)
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	header, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, nil, err
	}
	if !netDevHeader.Match(header) {
		return nil, nil, netDevErr
	}
	cols, err := reader.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	colSections := strings.Split(cols, "|")
	if len(colSections) != 3 {
		return nil, nil, netDevErr
	}
	receiveColNames := strings.Fields(colSections[1])
	transmitColNames := strings.Fields(colSections[2])
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if len(receive) == 0 {
					return nil, nil, errors.New("No devices")
				}
				break
			}
			return nil, nil, err
		}

		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			return nil, nil, netDevErr
		}
		key := string(strings.TrimSpace(parts[0]))
		receiveValue := make(map[string]uint64)
		transmitValue := make(map[string]uint64)
		fields := strings.Fields(parts[1])
		if len(fields) != len(receiveColNames)+len(transmitColNames) {
			return nil, nil, netDevErr
		}
		for i, field := range fields {
			v, err := strconv.ParseUint(string(field), 10, 64)
			if err != nil {
				return nil, nil, err
			}
			if i < len(receiveColNames) {
				receiveValue[receiveColNames[i]] = v
			} else {
				transmitValue[transmitColNames[i-len(receiveColNames)]] = v
			}
		}
		receive[key] = receiveValue
		transmit[key] = transmitValue
	}

	return receive, transmit, nil
}
