package main

import (
	"testing"
)

func TestDecomposeDevNumber(t *testing.T) {
	for _, tt := range []struct {
		dev  uint64
		want blockDev
	}{
		{dev: 66305, want: blockDev{259, 1}},
		{dev: 26266112, want: blockDev{202, 6400}},
		{dev: 51713, want: blockDev{202, 1}},
	} {
		if got := decomposeDevNumber(tt.dev); got != tt.want {
			t.Fatalf("got: %+v; want: %+v", got, tt.want)
		}
	}
}
