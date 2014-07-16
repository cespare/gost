package main

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
