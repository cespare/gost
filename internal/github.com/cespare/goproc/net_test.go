package proc

import (
	"testing"
)

func TestNetProtoStats(t *testing.T) {
	stats, err := NetProtoStats()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Ip", "Tcp", "Udp"} {
		if _, ok := stats[key]; !ok {
			t.Errorf("Expected to find key key %q in net stats.", key)
		}
	}
}

func TestNetDevStats(t *testing.T) {
	receive, transmit, err := NetDevStats()
	if err != nil {
		t.Fatal(err)
	}
	if receive["lo"]["bytes"] <= 0 {
		t.Errorf("Expected a non-zero value for received bytes on lo")
	}
	if transmit["lo"]["packets"] <= 0 {
		t.Errorf("Expected a non-zero value for transmitted packets on lo")
	}
}
