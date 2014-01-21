package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Conf struct {
	GraphiteAddr             string       `toml:"graphite_addr"`
	ForwardingAddr           string       `toml:"forwarding_addr"`
	ForwarderListenAddr      string       `toml:"forwarder_listen_addr"`
	ForwardedNamespace       string       `toml:"forwarded_namespace"`
	Port                     int          `toml:"port"`
	DebugPort                int          `toml:"debug_port"`
	DebugLogging             bool         `toml:"debug_logging"`
	ClearStatsBetweenFlushes bool         `toml:"clear_stats_between_flushes"`
	FlushIntervalMS          int          `toml:"flush_interval_ms"`
	Namespace                string       `toml:"namespace"`
	OSStats                  *OSStatsConf `toml:"os_stats"`
	Scripts                  *ScriptsConf `toml:"scripts"`
}

type ScriptsConf struct {
	Path          string `toml:"path"`
	RunIntervalMS int    `toml:"run_interval_ms"`
}

type OSStatsConf struct {
	CheckIntervalMS int                  `toml:"check_interval_ms"`
	Mem             *MemConf             `toml:"mem"`
	CPU             *CPUConf             `toml:"cpu"`
	Net             *NetConf             `toml:"net"`
	Disk            map[string]*DiskConf `toml:"disk"`
}

type MemConf struct {
	Breakdown string `toml:"breakdown"`
}

type CPUConf struct {
	Stat    bool   `toml:"stat"`
	LoadAvg string `toml:"load_avg"`
}

type NetConf struct {
	TCP     bool     `toml:"tcp"`
	UDP     bool     `toml:"udp"`
	Devices []string `toml:"devices"`
}

type DiskConf struct {
	Path  string `toml:"path"`
	Usage string `toml:"usage"`
	IO    bool   `toml:"io"`
}

// filterNamespace replaces templated fields in the user-provided namespace and sanitizes it.
func filterNamespace(ns string) string {
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	ns = strings.NewReplacer("%H", hostname).Replace(ns)
	sanitized, ok, _, rest := parseKey([]byte(ns + ":"))
	if !ok || len(rest) > 0 {
		log.Fatal("Bad tag:", ns)
	}
	return sanitized
}

func parseConf() {
	conf = &Conf{}
	f, err := os.Open(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	meta, err := toml.DecodeReader(f, conf)
	if err != nil {
		log.Fatalf("Error decoding %s: %s", *configFile, err)
	}

	for _, field := range []string{"graphite_addr", "port", "debug_port", "flush_interval_ms", "namespace"} {
		if !meta.IsDefined(field) {
			log.Fatal(field, "is required")
		}
	}
	if conf.FlushIntervalMS <= 0 {
		log.Fatal("flush_interval_ms must be positive")
	}

	if meta.IsDefined("forwarding_addr") {
		forwardingEnabled = true
	}

	if meta.IsDefined("forwarder_listen_addr") {
		forwarderEnabled = true
		if !meta.IsDefined("forwarded_namespace") {
			log.Fatal("forwarded_namespace is required if gost is configured as a forwarder")
		}
	}

	if err := validateOSStatsConf(conf.OSStats, meta); err != nil {
		log.Fatal(err)
	}
	if !meta.IsDefined("os_stats", "check_interval_ms") {
		conf.OSStats.CheckIntervalMS = conf.FlushIntervalMS
	}
	if err := validateScriptsConf(conf.Scripts, meta); err != nil {
		log.Fatal(err)
	}

	conf.Namespace = filterNamespace(conf.Namespace)
	conf.ForwardedNamespace = filterNamespace(conf.ForwardedNamespace)
}

func validateOSStatsConf(osStats *OSStatsConf, meta toml.MetaData) error {
	if osStats == nil {
		return nil
	}
	if meta.IsDefined("os_stats", "check_interval_ms") {
		if osStats.CheckIntervalMS <= 0 {
			return errors.New("check_interval_ms must be positive")
		}
	}
	if err := validateMemConf(osStats.Mem); err != nil {
		return err
	}
	if err := validateCPUConf(osStats.CPU); err != nil {
		return err
	}
	// For now, any parseable NetConf is valid.
	for _, diskConf := range osStats.Disk {
		if err := validateDiskConf(diskConf); err != nil {
			return err
		}
	}
	return nil
}

func validateMemConf(memConf *MemConf) error {
	if memConf == nil {
		return nil
	}
	switch memConf.Breakdown {
	case "", "fraction", "breakdown":
	default:
		return fmt.Errorf("Bad 'breakdown' value for os_stats.mem: %q", memConf.Breakdown)
	}
	return nil
}

func validateCPUConf(cpuConf *CPUConf) error {
	if cpuConf == nil {
		return nil
	}
	switch cpuConf.LoadAvg {
	case "", "total", "per_cpu":
	default:
		return fmt.Errorf("Bad 'load_avg' value for os_stats.cpu: %q", cpuConf.LoadAvg)
	}
	return nil
}

func validateDiskConf(diskConf *DiskConf) error {
	if diskConf.Path == "" {
		return errors.New("Disk section without a path specified")
	}
	switch diskConf.Usage {
	case "", "fraction", "absolute":
	default:
		return fmt.Errorf("Bad 'usage' value for os_stats.disk.<device>: %q", diskConf.Usage)
	}
	return nil
}

func validateScriptsConf(scripts *ScriptsConf, meta toml.MetaData) error {
	if scripts == nil {
		return nil
	}
	if !meta.IsDefined("scripts", "path") {
		return errors.New("scripts section provided without path.")
	}
	if !meta.IsDefined("scripts", "run_interval_ms") {
		return errors.New("scripts section provided without run_interval_ms.")
	}
	if scripts.RunIntervalMS <= 0 {
		return errors.New("scripts.run_interval_ms must be positive")
	}
	return nil
}
