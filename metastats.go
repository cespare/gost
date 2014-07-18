package main

import (
	"log"
	"time"
)

// If this queue fills, gost crashes to avoid deadlock. (Expected size is very small, so a full queue is
// almost certainly a bug.)
const metaStatsQueueSize = 10000

func when(stat *Stat, ch chan *Stat) chan *Stat {
	if stat == nil {
		return nil
	}
	return ch
}

func (s *Server) handleMetaStats() {
	queue := make(chan *Stat, metaStatsQueueSize)
	var transientStat *Stat
	for {
		select {
		case stat := <-s.metaStats:
			if transientStat == nil {
				transientStat = stat
			} else {
				select {
				case queue <- stat:
				default:
					log.Fatal("meta-stats queue is full. Bug?")
				}
			}
		case when(transientStat, s.incoming) <- transientStat:
			select {
			case transientStat = <-queue:
			default:
				transientStat = nil
			}
		case <-s.quit:
			return
		}
	}
}

// metaCount advances a counter for an internal gost stat.
func (s *Server) metaCount(name string, value float64) {
	s.metaStats <- &Stat{
		Type:       StatCounter,
		Name:       "gost." + name,
		Value:      value,
		SampleRate: 1.0,
	}
}

func (s *Server) metaInc(name string) { s.metaCount(name, 1) }

func (s *Server) metaTimer(name string, elapsed time.Duration) {
	s.metaStats <- &Stat{
		Type:  StatTimer,
		Name:  "gost." + name,
		Value: elapsed.Seconds() * 1000,
	}
}
