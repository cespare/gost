package main

import (
	"log"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// filterNamespace replaces templated fields in the user-provided namespace and sanitizes it.
func filterNamespace(ns string) string {
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	ns = strings.NewReplacer("%H", hostname).Replace(ns)
	return sanitizeKey(ns)
}

func parseConf() {
	conf = &Conf{}
	f, err := os.Open(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	meta, err := toml.DecodeReader(f, conf)
	if err != nil {
		log.Fatalf("Error decoding %s: %s\n", *configFile, err)
	}

	for _, field := range []string{"graphite_addr", "port", "flush_interval_ms", "namespace"} {
		if !meta.IsDefined(field) {
			log.Fatal(field, "is required")
		}
	}
	if conf.FlushIntervalMS <= 0 {
		log.Fatal("flush_interval_ms must be positive")
	}

	osStats := conf.OsStats
	if osStats != nil {
		if meta.IsDefined("os_stats", "check_interval_ms") {
			if osStats.CheckIntervalMS <= 0 {
				log.Fatal("check_interval_ms must be positive")
			}
		} else {
			osStats.CheckIntervalMS = conf.FlushIntervalMS
		}
		for _, field := range [][]int{osStats.LoadAvg, osStats.LoadAvgPerCPU} {
			for _, t := range field {
				switch t {
				case 1, 5, 15:
				default:
					log.Fatalf("bad load average time window: %d\n", t)
				}
			}
		}
		for name, options := range osStats.DiskUsage {
			if options == nil {
				log.Fatalf("bad disk usage section %s.\n", name)
			}
			for _, field := range []string{"path", "values"} {
				if !meta.IsDefined("os_stats", "disk_usage", name, field) {
					log.Fatalf("missing %s in disk usage section %s.\n", field, name)
				}
			}
			switch options.Values {
			case "fraction", "absolute":
			default:
				log.Fatalf("bad values given: %s (must be 'fraction' or 'absolute')", options.Values)
			}
		}
	}

	parts := strings.Split(conf.Namespace, ".")
	namespace = make([]string, len(parts))
	for i, part := range parts {
		namespace[i] = filterNamespace(part)
	}
}
