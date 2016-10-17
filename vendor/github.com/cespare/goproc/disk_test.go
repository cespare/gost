package proc

import (
	"syscall"
	"testing"
)

func TestMounts(t *testing.T) {
	mounts, err := Mounts()
	if err != nil {
		t.Fatal(err)
	}
	// Check that there's a / entry.
	found := false
	for _, entry := range mounts {
		if entry.File == "/" && entry.Vfstype != "rootfs" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Could not locate / in mounts")
	}
}

func TestDiskStats(t *testing.T) {
	var statt syscall.Stat_t
	if err := syscall.Stat("/", &statt); err != nil {
		t.Fatal(err)
	}
	major, minor := decomposeDevNumber(statt.Dev)

	stats, err := DiskStats()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, stat := range stats {
		if stat.Major == major && stat.Minor == minor {
			found = true
			if stat.ReadsCompleted == 0 {
				t.Errorf("Expected non-zero completed reads for root partition disk.")
			}
		}
	}
	if !found {
		t.Errorf("Partition (%d, %d) (mounted at /) was not in /proc/diskstats", major, minor)
	}
}

func decomposeDevNumber(n uint64) (major, minor int) {
	minor = int(n & 0xff)
	major = int((n & 0xff00) >> 8)
	return major, minor
}
