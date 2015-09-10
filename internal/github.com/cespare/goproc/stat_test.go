package proc

import (
	"testing"
)

func TestStat(t *testing.T) {
	stat, err := Stat()
	if err != nil {
		t.Fatal(err)
	}
	if stat.Cpu == nil || stat.Cpu.User == 0 || stat.Cpu.System == 0 || stat.Cpu.Idle == 0 {
		t.Error("Unexpected CPU info from Stat().")
	}
}
