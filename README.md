# gost

Gost is a Go implementation of the [StatsD](https://github.com/etsy/statsd/) daemon.

## Usage

TODO

## Differences with StatsD

* Gauges cannot be deltas; they must be absolute values.
* No stats will be sent if there is no data for a flush interval. (In StatsD, this is like setting
  `deleteCounters`, `deleteTimers`, etc.)
* Timers don't return as much information as in statsd, and they're not customizable.

# To Do

* Do I want to implement fancy stats on timers? (quartiles or even custom bins for histograms).
* System stats (CPU load? HD space usage? ...)
* Hostname parsing for namespaces
* Right now we clear all stats by default, and don't send zero values (equivalent to statsd's
  config.deleteCounters, etc). Do we ever want to send zero values?
* TCP management interface. Is this useful?
