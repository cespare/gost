package proc

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

// FSTabEntry describes a line from /proc/mounts, which is the fstab format. See 'man fstab' for the meaning
// of these fields.
// The field comments are examples of what might be found there (but see the man page for details).
type FSTabEntry struct {
	Spec    string   // /dev/sda1
	File    string   // /mnt/data
	Vfstype string   // ext4
	Mntops  []string // [rw, relatime]
	Freq    int      // 0
	Passno  int      // 0
}

var mountsErr = errors.New("Cannot parse /proc/mounts")

// Read mount information from /proc/mounts for the current process.
// BUG(caleb): This doesn't handle spaces in mount points, even though fstab specifies an encoding for them.
func Mounts() ([]*FSTabEntry, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	results := []*FSTabEntry{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) != 6 {
			return nil, mountsErr
		}
		freq, err := strconv.Atoi(fields[4])
		if err != nil {
			return nil, err
		}
		passno, err := strconv.Atoi(fields[5])
		if err != nil {
			return nil, err
		}
		results = append(results, &FSTabEntry{
			Spec:    fields[0],
			File:    fields[1],
			Vfstype: fields[2],
			Mntops:  strings.Split(fields[3], ","),
			Freq:    freq,
			Passno:  passno,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// IOStatEntry represents a line in /proc/diskstats.
type IOStatEntry struct {
	// Partition info
	Major int
	Minor int
	Name  string
	// The rest of the fields are described in Linux's Documentation/iostats.txt.
	ReadsCompleted   uint64
	ReadsMerged      uint64
	SectorsRead      uint64
	ReadMillis       uint64
	WritesCompleted  uint64
	WritesMerged     uint64
	SectorsWritten   uint64
	WriteMillis      uint64
	NumInProgressIOs uint64
	IOMillis         uint64
	WeightedIOMillis uint64
}

var diskStatsErr = errors.New("Cannot parse /proc/diskstats")

// DiskStats reports disk information from /proc/diskstats.
func DiskStats() ([]*IOStatEntry, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(f)
	stats := []*IOStatEntry{}
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) != 14 {
			return nil, diskStatsErr
		}
		major, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, err
		}
		minor, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		statFields := make([]uint64, len(fields)-3)
		for i, field := range fields[3:] {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return nil, err
			}
			statFields[i] = v
		}
		entry := &IOStatEntry{
			Major:            major,
			Minor:            minor,
			Name:             fields[2],
			ReadsCompleted:   statFields[0],
			ReadsMerged:      statFields[1],
			SectorsRead:      statFields[2],
			ReadMillis:       statFields[3],
			WritesCompleted:  statFields[4],
			WritesMerged:     statFields[5],
			SectorsWritten:   statFields[6],
			WriteMillis:      statFields[7],
			NumInProgressIOs: statFields[8],
			IOMillis:         statFields[9],
			WeightedIOMillis: statFields[10],
		}
		stats = append(stats, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}
