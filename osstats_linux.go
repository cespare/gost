package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"

	proc "github.com/cespare/gost/internal/github.com/cespare/goproc"
)

var (
	loadAvgTypes = []int{1, 5, 15}
	nCPU         = float64(runtime.NumCPU())
)

type OSData struct {
	// NOTE(caleb): Many of the stats (acquired from the proc filesystem) are global counters, and to get
	// meaningful data out of them, you have to observe the value and then look at the delta later. These
	// variables store the previously observed counter values.
	cpuStats        counterStats
	diskDeviceStats map[partition]counterStats
	tcpStats        counterStats
	udpStats        counterStats
	netDeviceStats  map[string]counterStats
}

func (s *Server) InitOSData() {
	s.osData.diskDeviceStats = make(map[partition]counterStats)
	s.osData.netDeviceStats = make(map[string]counterStats)
}

// Linux counter stats represented by unsigned ints/longs. These can roll over.
type counterStats []uint64

// A partition identifies a machine partition by major and minor device numbers.
type partition [2]int

// Sub subtracts two counterStats, assuming they are the same length.
// TODO(caleb): This should probably handle integer rollover. Different proc counters might have different
// counter sizes, though.
func (s counterStats) Sub(other counterStats) counterStats {
	d := make(counterStats, len(s))
	for i, v := range s {
		d[i] = v - other[i]
	}
	return d
}

func (s *Server) reportMemStats() error {
	memInfo, err := proc.MemInfo()
	if err != nil {
		return err
	}
	total := float64(memInfo["MemTotal"])
	cached := float64(memInfo["Cached"])
	used := total - cached - float64(memInfo["MemFree"]+memInfo["Buffers"])
	if s.conf.OSStats.Mem.Breakdown == "fraction" {
		used /= total
		cached /= total
	}
	s.osGauge("mem.used", used)
	s.osGauge("mem.cached", cached)
	return nil
}

func (s *Server) reportCPUStats() error {
	// First, CPU time percentages for user, nice, system, and iowait.
	if s.conf.OSStats.CPU.Stat {
		stat, err := proc.Stat()
		if err != nil {
			return err
		}
		newStats := cpuStatInfoTocounterStats(stat.Cpu)
		if s.osData.cpuStats != nil {
			// On the first run, just save the stats; nothing to report yet.
			diff := newStats.Sub(s.osData.cpuStats)
			var total uint64
			for _, v := range diff {
				total += v
			}
			totalFloat := float64(total)
			s.osGauge("cpu.stat.user", float64(diff[0])/totalFloat)
			s.osGauge("cpu.stat.nice", float64(diff[1])/totalFloat)
			s.osGauge("cpu.stat.system", float64(diff[2])/totalFloat)
			s.osGauge("cpu.stat.iowait", float64(diff[4])/totalFloat)
		}
		s.osData.cpuStats = newStats
	}

	// Next, load averages.
	if format := s.conf.OSStats.CPU.LoadAvg; format != "" {
		loadAverages, err := proc.LoadAverages()
		if err != nil {
			return err
		}
		switch format {
		case "total":
			for i, avg := range loadAverages {
				s.osGauge(fmt.Sprintf("cpu.load_avg_%d", loadAvgTypes[i]), avg)
			}
		case "per_cpu":
			for i, avg := range loadAverages {
				s.osGauge(fmt.Sprintf("cpu.load_avg_per_cpu_%d", loadAvgTypes[i]), avg/nCPU)
			}
		}
	}

	return nil
}

func (s *Server) reportNetStats() error {
	if s.conf.OSStats.Net.TCP || s.conf.OSStats.Net.UDP {
		netProtoStats, err := proc.NetProtoStats()
		if err != nil {
			return err
		}

		if s.conf.OSStats.Net.TCP {
			stats, ok := netProtoStats["Tcp"]
			if !ok {
				return errors.New("Cannot determine TCP stats using package proc")
			}
			s.osGauge("net.tcp.current_connections", float64(stats["CurrEstab"]))
			newStats := counterStats{uint64(stats["ActiveOpens"]), uint64(stats["PassiveOpens"])}
			if s.osData.tcpStats != nil {
				diff := newStats.Sub(s.osData.tcpStats)
				s.osCounter("net.tcp.active_opens", float64(diff[0]))
				s.osCounter("net.tcp.passive_opens", float64(diff[1]))
			}
			s.osData.tcpStats = newStats
		}

		if s.conf.OSStats.Net.UDP {
			stats, ok := netProtoStats["Udp"]
			if !ok {
				return errors.New("Cannot determine UDP stats using package proc")
			}
			newStats := counterStats{uint64(stats["InDatagrams"]), uint64(stats["OutDatagrams"])}
			if s.osData.udpStats != nil {
				diff := newStats.Sub(s.osData.udpStats)
				s.osCounter("net.udp.in_datagrams", float64(diff[0]))
				s.osCounter("net.udp.out_datagrams", float64(diff[1]))
			}
			s.osData.udpStats = newStats
		}
	}

	if len(s.conf.OSStats.Net.Devices) > 0 {
		receiveStats, transmitStats, err := proc.NetDevStats()
		if err != nil {
			return err
		}
		for _, device := range s.conf.OSStats.Net.Devices {
			deviceReceiveStats, ok := receiveStats[device]
			if !ok {
				return fmt.Errorf("Cannot determine receive stats for %s", device)
			}
			deviceTransmitStats, ok := transmitStats[device]
			if !ok {
				return fmt.Errorf("Cannot determine transmit stats for %s", device)
			}
			newCounters := counterStats{
				deviceReceiveStats["bytes"], deviceTransmitStats["bytes"],
				deviceReceiveStats["packets"], deviceTransmitStats["packets"],
				deviceReceiveStats["errors"], deviceTransmitStats["errors"],
			}
			if oldCounters, ok := s.osData.netDeviceStats[device]; ok {
				diff := newCounters.Sub(oldCounters)
				for i, name := range []string{
					"receive_bytes", "transmit_bytes",
					"receive_packets", "transmit_packets",
					"receive_errors", "transmit_errors",
				} {
					s.osCounter("net.devices."+device+"."+name, float64(diff[i]))
				}
			}
			s.osData.netDeviceStats[device] = newCounters
		}
	}

	return nil
}

func (s *Server) reportDiskStats() error {
	for name, options := range s.conf.OSStats.Disk {
		if options.Usage != "" {
			statfsInfo := &syscall.Statfs_t{}
			if err := syscall.Statfs(options.Path, statfsInfo); err != nil {
				return err
			}
			usedBlocks := statfsInfo.Blocks - statfsInfo.Bavail // blocks used
			var used float64
			if options.Usage == "absolute" {
				used = float64(usedBlocks * uint64(statfsInfo.Bsize)) // number of bytes used
			} else {
				used = float64(usedBlocks) / float64(statfsInfo.Blocks) // fraction of space used
			}
			s.osGauge("disk."+name+".usage", used)
		}

		if options.IO {
			statInfo := &syscall.Stat_t{}
			if err := syscall.Stat(options.Path, statInfo); err != nil {
				return err
			}
			device := decomposeDevNumber(statInfo.Dev)
			diskStats, err := proc.DiskStats()
			if err != nil {
				return err
			}
			var stats *proc.IOStatEntry
			for _, entry := range diskStats {
				if entry.Major == device[0] && entry.Minor == device[1] {
					stats = entry
				}
			}
			if stats == nil {
				return fmt.Errorf("Cannot determine stats for device at %s", options.Path)
			}
			newStats := counterStats{
				stats.ReadsCompleted, stats.SectorsRead,
				stats.WritesCompleted, stats.SectorsWritten,
			}
			if oldStats, ok := s.osData.diskDeviceStats[device]; ok {
				diff := newStats.Sub(oldStats)
				s.osCounter("disk."+name+".io.reads", float64(diff[0]))
				s.osCounter("disk."+name+".io.writes", float64(diff[2]))
				// NOTE(caleb): As far as I can tell, a "sector" (in the context of /proc/diskstats and iostat) is 512
				// bytes. See, for example, `man iostat`.
				s.osCounter("disk."+name+".io.read_bytes", float64(diff[1])*512)
				s.osCounter("disk."+name+".io.write_bytes", float64(diff[3])*512)
			}
			s.osData.diskDeviceStats[device] = newStats
		}
	}

	return nil
}

// decomposeDevNumber pulls the major and minor device numbers (last two bytes) from a single uint64.
func decomposeDevNumber(n uint64) partition {
	minor := int(n & 0xff)
	major := int((n & 0xff00) >> 8)
	return partition{major, minor}
}

func (s *Server) checkOSStats() {
	s.reportOSStats()
	ticker := time.NewTicker(time.Duration(s.conf.OSStats.CheckIntervalMS) * time.Millisecond)
	for {
		select {
		case <-ticker.C:
			s.reportOSStats()
		case <-s.quit:
			return
		}
	}
}

func (s *Server) reportOSStats() {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		// Use a counter here instead of the full expense of a timer.
		s.metaCount("os_stats_check_duration_ms", elapsed.Seconds()*1000)
	}()
	if s.conf.OSStats.Mem != nil {
		if err := s.reportMemStats(); err != nil {
			s.metaInc("errors.os_stats_mem_check")
			s.l.Debugln("mem stats check failure:", err)
		}
	}
	if s.conf.OSStats.CPU != nil {
		if err := s.reportCPUStats(); err != nil {
			s.metaInc("errors.os_stats_cpu_check")
			s.l.Debugln("cpu stats check failure:", err)
		}
	}
	if s.conf.OSStats.Net != nil {
		if err := s.reportNetStats(); err != nil {
			s.metaInc("errors.os_stats_net_check")
			s.l.Debugln("net stats check failure:", err)
		}
	}
	if s.conf.OSStats.Disk != nil {
		if err := s.reportDiskStats(); err != nil {
			s.metaInc("errors.os_stats_disk_check")
			s.l.Debugln("disk stats check failure:", err)
		}
	}
}

func cpuStatInfoTocounterStats(cpuStats *proc.CPUStatInfo) counterStats {
	return counterStats{
		cpuStats.User, cpuStats.Nice, cpuStats.System, cpuStats.Idle, cpuStats.Iowait, cpuStats.Irq,
		cpuStats.Softirq, cpuStats.Steal, cpuStats.Guest, cpuStats.Guest_nice,
	}
}
