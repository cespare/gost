# gost

Gost is a Go implementation of the [StatsD](https://github.com/etsy/statsd/) daemon.

# To Do

* Implement sets
* Implement gauges
* Implement timers
* Implement fancy stats on timers (histograms, etc)
* Meta-stats (about how many stats were flushed, etc)
  - bad lines seen (counter)
  - packets received (counter)
  - number of stats being tracked
  - Per flush:
    * calculation time (timer)
    * processing time (timer)
    * flush time/length
* System stats (CPU load? HD usage? ...)
* Key sanitization
* Hostname parsing for namespaces
* Right now we clear all stats by default, and don't send zero values (equivalent to statsd's
  config.deleteCounters, etc). Do we ever want to send zero values?
* TCP management interface
