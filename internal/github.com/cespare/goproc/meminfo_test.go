package proc

import (
	"testing"
)

func TestMemInfo(t *testing.T) {
	meminfo, err := MemInfo()
	if err != nil {
		t.Fatal(err)
	}
	// This test can only be run on modern machines
	if meminfo["MemTotal"] < 1e8 {
		t.Error("Expected MemTotal to show at least 256MB.")
	}
}
