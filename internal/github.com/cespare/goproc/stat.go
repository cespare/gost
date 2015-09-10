package proc

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// StatInfo represents entries in /proc/stat.
type StatInfo struct {
	Cpu           *CPUStatInfo
	Cpus          []*CPUStatInfo
	Intr          []uint64
	Ctxt          uint64
	Btime         time.Time
	Processes     uint64
	Procs_running uint64
	Procs_blocked uint64
}

// A CPUStatInfo is a CPU entry in /proc/stat. All values are in USER_HZ.
type CPUStatInfo struct {
	User       uint64
	Nice       uint64
	System     uint64
	Idle       uint64
	Iowait     uint64
	Irq        uint64
	Softirq    uint64
	Steal      uint64
	Guest      uint64
	Guest_nice uint64
}

// Stat returns kernel and CPU statistics as reported by /proc/stat.
func Stat() (*StatInfo, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	stat := &StatInfo{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, values, err := parseStatLine(scanner.Text())
		switch key {
		case "cpu":
			stat.Cpu, err = makeCPUStatInfo(values)
			if err != nil {
				return nil, err
			}
			continue
		case "intr":
			stat.Intr = values
			continue
		}
		if strings.HasPrefix(key, "cpu") {
			n, err := strconv.Atoi(strings.TrimPrefix(key, "cpu"))
			if err != nil {
				return nil, statErr
			}
			cpuStatInfo, err := makeCPUStatInfo(values)
			if err != nil {
				return nil, statErr
			}
			// Sanity check
			// TODO(caleb): Update this when our cpus have > 1024 cores :)
			if n > 1024 {
				return nil, statErr
			}
			if cap(stat.Cpus) < n+1 {
				newCpus := make([]*CPUStatInfo, n, 2*(n+1))
				copy(newCpus, stat.Cpus)
				stat.Cpus = newCpus
			}
			stat.Cpus = stat.Cpus[:n+1]
			stat.Cpus[n] = cpuStatInfo
			continue
		}
		switch key {
		case "ctxt":
			if len(values) != 1 {
				return nil, statErr
			}
			stat.Ctxt = values[0]
		case "btime":
			if len(values) != 1 {
				return nil, statErr
			}
			stat.Btime = time.Unix(int64(values[0]), 0)
		case "processes":
			if len(values) != 1 {
				return nil, statErr
			}
			stat.Processes = values[0]
		case "procs_running":
			if len(values) != 1 {
				return nil, statErr
			}
			stat.Procs_running = values[0]
		case "procs_blocked":
			if len(values) != 1 {
				return nil, statErr
			}
			stat.Procs_blocked = values[0]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return stat, nil
}

var statErr = errors.New("Cannot parse /proc/stats")

func parseStatLine(line string) (key string, values []uint64, err error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", nil, statErr
	}
	values = make([]uint64, len(fields)-1)
	for i, field := range fields[1:] {
		values[i], err = strconv.ParseUint(field, 10, 64)
		if err != nil {
			return "", nil, err
		}
	}
	return fields[0], values, nil
}

func makeCPUStatInfo(values []uint64) (*CPUStatInfo, error) {
	if len(values) > 10 {
		return nil, statErr
	}
	statInfo := &CPUStatInfo{}
	for i, v := range values {
		switch i {
		case 0:
			statInfo.User = v
		case 1:
			statInfo.Nice = v
		case 2:
			statInfo.System = v
		case 3:
			statInfo.Idle = v
		case 4:
			statInfo.Iowait = v
		case 5:
			statInfo.Irq = v
		case 6:
			statInfo.Softirq = v
		case 7:
			statInfo.Steal = v
		case 8:
			statInfo.Guest = v
		case 9:
			statInfo.Guest_nice = v
		}
	}
	return statInfo, nil
}
