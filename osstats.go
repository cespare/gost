package main

const (
	majorMask uint64 = 0xfff
	minorMask uint64 = 0xff
)

// A blockDev is a Linux block device number.
type blockDev struct {
	major int
	minor int
}

// decomposeDevNumber extracts the major and minor device numbers for a Linux
// block device. See the major/minor macros in Linux's sysmacros.h.
func decomposeDevNumber(dev uint64) blockDev {
	return blockDev{
		major: int(((dev >> 8) & majorMask) | ((dev >> 32) & ^majorMask)),
		minor: int((dev & minorMask) | ((dev >> 12) & ^minorMask)),
	}
}

func (s *Server) osGauge(name string, value float64) {
	s.incoming <- &Stat{
		Type:  StatGauge,
		Name:  "gost.os_stats." + name,
		Value: value,
	}
}

func (s *Server) osCounter(name string, value float64) {
	s.incoming <- &Stat{
		Type:       StatCounter,
		Name:       "gost.os_stats." + name,
		Value:      value,
		SampleRate: 1.0,
	}
}
