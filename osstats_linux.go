package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"syscall"
	"time"

	proc "github.com/cespare/goproc"
)

var (
	loadAvgTypes = []int{1, 5, 15}
	nCPU float64
)

// init obtains nCPU using `proc.Stat()`, because `runtime.NumCPU()` doesn't
// account for isolcpus.
func init() {
	stat, err := proc.Stat()
	if err != nil {
		log.Fatal("Cannot obtain CPU stats: ", err)
	}
	nCPU = float64(len(stat.Cpus))
}

type OSData struct {
	netDevices []string // e.g., ["lo", "eth0"]
	// NOTE(caleb): Many of the stats (acquired from the proc filesystem)
	// are global counters, and to get meaningful data out of them, you have
	// to observe the value and then look at the delta later. These
	// variables store the previously observed counter values.
	cpuStats        counterStats
	diskDeviceStats map[blockDev]counterStats
	tcpStats        counterStats
	udpStats        counterStats
	netDeviceStats  map[string]counterStats
}

func (s *Server) InitOSData() {
	if netDevices, err := findNetDevices(); err == nil {
		s.osData.netDevices = netDevices
	} else {
		log.Println("Error discovering network devices:", err)
	}
	s.osData.diskDeviceStats = make(map[blockDev]counterStats)
	s.osData.netDeviceStats = make(map[string]counterStats)
}

// Linux counter stats represented by unsigned ints/longs. These can roll over.
type counterStats []uint64

// Sub subtracts two counterStats, assuming they are the same length.
// TODO(caleb): This should probably handle integer rollover. Different proc
// counters might have different counter sizes, though.
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
	s.osGauge("mem.used", used/total)
	s.osGauge("mem.cached", cached/total)
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
	if s.conf.OSStats.CPU.LoadAvg {
		loadAverages, err := proc.LoadAverages()
		if err != nil {
			return err
		}
		for i, avg := range loadAverages {
			s.osGauge(fmt.Sprintf("cpu.load_avg_per_cpu_%d", loadAvgTypes[i]), avg/nCPU)
		}
	}

	return nil
}

func findNetDevices() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var devices []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 || iface.HardwareAddr != nil {
			devices = append(devices, iface.Name)
		}
	}
	return devices, nil
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
			newStats := counterStats{
				uint64(stats["InDatagrams"]),
				uint64(stats["OutDatagrams"]),
				uint64(stats["InErrors"]),
			}
			if s.osData.udpStats != nil {
				diff := newStats.Sub(s.osData.udpStats)
				s.osCounter("net.udp.in_datagrams", float64(diff[0]))
				s.osCounter("net.udp.out_datagrams", float64(diff[1]))
				s.osCounter("net.udp.in_errors", float64(diff[2]))
			}
			s.osData.udpStats = newStats
		}
	}

	if s.conf.OSStats.Net.Devices {
		rxStats, txStats, err := proc.NetDevStats()
		if err != nil {
			return err
		}
		for _, dev := range s.osData.netDevices {
			rx, ok := rxStats[dev]
			if !ok {
				return fmt.Errorf("Cannot determine receive stats for %s", dev)
			}
			tx, ok := txStats[dev]
			if !ok {
				return fmt.Errorf("Cannot determine transmit stats for %s", dev)
			}
			newCounters := counterStats{
				rx["bytes"], tx["bytes"],
				rx["packets"], tx["packets"],
				rx["errors"], tx["errors"],
			}
			if oldCounters, ok := s.osData.netDeviceStats[dev]; ok {
				diff := newCounters.Sub(oldCounters)
				for i, name := range []string{
					"receive_bytes", "transmit_bytes",
					"receive_packets", "transmit_packets",
					"receive_errors", "transmit_errors",
				} {
					s.osCounter("net.devices."+dev+"."+name, float64(diff[i]))
				}
			}
			s.osData.netDeviceStats[dev] = newCounters
		}
	}

	return nil
}

// A note about disk usage calculations:
//
// The statfs syscall gives us 3 relevant numbers:
//
// - number of blocks (f_blocks)
// - free blocks (f_bfree)
// - blocks available to non-privileged users (f_bavail)
//
// Note that avail is slightly less than free. To calculate the used percentage
// of the disk, we simply use
//
//   (blocks - avail) / blocks
//
// That is, the number of non-available blocks as a fraction of the total number
// of blocks.
//
// On the other hand, df computes the used percentage as
//
//   (blocks - free) / (blocks - free + avail)
//
// The difference between these computations hinges on blocks which are
// considered free but not available. df ignores these blocks entirely for the
// purpose of the computation; we include them (and count them as used).
// Both calculations seem fairly logical, and ours seems more easily explained.
// This is why gost gives disk usage percentages that differ slightly from df.

func (s *Server) reportDiskStats() error {
	for name, options := range s.conf.OSStats.Disk {
		if options.Usage {
			statfsInfo := &syscall.Statfs_t{}
			if err := syscall.Statfs(options.Path, statfsInfo); err != nil {
				return err
			}
			// See note about these calculations, above.
			usedBlocks := statfsInfo.Blocks - statfsInfo.Bavail
			used := float64(usedBlocks) / float64(statfsInfo.Blocks)
			s.osGauge("disk."+name+".usage", used)
		}

		if options.IO {
			statInfo := &syscall.Stat_t{}
			if err := syscall.Stat(options.Path, statInfo); err != nil {
				return err
			}
			bd := decomposeDevNumber(statInfo.Dev)
			diskStats, err := proc.DiskStats()
			if err != nil {
				return err
			}
			var stats *proc.IOStatEntry
			for _, entry := range diskStats {
				if entry.Major == bd.major && entry.Minor == bd.minor {
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
			if oldStats, ok := s.osData.diskDeviceStats[bd]; ok {
				diff := newStats.Sub(oldStats)
				s.osCounter("disk."+name+".io.reads", float64(diff[0]))
				s.osCounter("disk."+name+".io.writes", float64(diff[2]))
				// NOTE(caleb): As far as I can tell, a "sector"
				// (in the context of /proc/diskstats and iostat)
				// is 512 bytes. See, for example, `man iostat`.
				s.osCounter("disk."+name+".io.read_bytes", float64(diff[1])*512)
				s.osCounter("disk."+name+".io.write_bytes", float64(diff[3])*512)
			}
			s.osData.diskDeviceStats[bd] = newStats
		}
	}

	return nil
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
	if s.conf.OSStats.Mem {
		if err := s.reportMemStats(); err != nil {
			s.metaInc("errors.os_stats_mem_check")
			log.Println("mem stats check failure:", err)
		}
	}
	if s.conf.OSStats.CPU != nil {
		if err := s.reportCPUStats(); err != nil {
			s.metaInc("errors.os_stats_cpu_check")
			log.Println("cpu stats check failure:", err)
		}
	}
	if s.conf.OSStats.Net != nil {
		if err := s.reportNetStats(); err != nil {
			s.metaInc("errors.os_stats_net_check")
			log.Println("net stats check failure:", err)
		}
	}
	if s.conf.OSStats.Disk != nil {
		if err := s.reportDiskStats(); err != nil {
			s.metaInc("errors.os_stats_disk_check")
			log.Println("disk stats check failure:", err)
		}
	}
}

func cpuStatInfoTocounterStats(cpuStats *proc.CPUStatInfo) counterStats {
	return counterStats{
		cpuStats.User, cpuStats.Nice, cpuStats.System, cpuStats.Idle, cpuStats.Iowait, cpuStats.Irq,
		cpuStats.Softirq, cpuStats.Steal, cpuStats.Guest, cpuStats.Guest_nice,
	}
}
