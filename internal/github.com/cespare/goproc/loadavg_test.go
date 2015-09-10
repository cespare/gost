package proc

import (
	"runtime"
	"testing"
)

func TestLoadAverages(t *testing.T) {
	avgs, err := LoadAverages()
	if err != nil {
		t.Fatal(err)
	}
	// Basic sanity check
	ncpu := runtime.NumCPU()
	for _, load := range avgs {
		if load <= 0 || load > float64(ncpu)*5 {
			t.Errorf("Bad load average: %.2f", load)
		}
	}
}
