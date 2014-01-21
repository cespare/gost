# gost

Gost is a Go implementation of the [StatsD](https://github.com/etsy/statsd/) daemon.

## Usage

Right now there's no great installation method; you'll have to install from source:

    $ go get github.com/cespare/gost

Run `gost` with a conf file.

    $ gost -conf /my/config.toml

By default it uses `conf.toml`. This repo includes a [`conf.toml`](conf.toml) that should get you started. It
has a lot of comments that explain what all the options are.

### Messages

Gost is largely statsd compatible and any statsd library you want should work with it out of the box. The main
API difference is that gauges cannot be delta values (they are always interpreted as absolute).

For completeness, here is a summary of the supported messages. All messages are sent via UDP to localhost on a
port configured by the `port` setting in the config file. Typically each message is a UDP packet, but multiple
messages can be sent in a single packet by separating them with `\n` characters.

There are two data types involved: **keys** and **values**. **keys** are ascii strings (see the Key Format
section below for details). **values** are human-printed floats:

    /^[+\-]?\d+(\.\d+)?$/

Counters have a sampling rate, which is the same format as a value. This tells gost that the counter is being
sampled at some rate, and gost divides the counter value by the sampling rate to obtain an estimate of the
true value.

**Counters**

A counter records occurrences of some event, or other values that can be accumulated by summing them.

For each counter, gost records two metrics:

* `count`: the raw counts (scaled for sample rate)
* `rate`: the rate, per second

Syntax: `<key>:<value>|c(|@<sampling-rate>)?`

Examples:

    rails.requests:1|c
    page_hits:135|c|@0.1

**Timers**

Timers are for measuring the elapsed time of some operation. These are more complex than the other kinds of
stats. For each timer key, gost records the following metrics during each flush period:

* `timer.count`: the number of timer calls that have been recorded
* `timer.rate`: the rate at which timer calls came in, per second
* `timer.min`, `timer.max`: the min and max values of the timer during the flush interval
* `timer.mean`, `timer.median`, `timer.stdev`: the mean, median, and standard deviation, respectively, of
  the timer values during the flush interval
* `timer.sum`: the total sum of all timer values during the interval. This value, in concert with
  `timer.count`, can be used (by some other system) to compute mean values across flush buckets.

Syntax: `<key>:<value>|ms`

Example: `s3_backup:1411|ms`

**Gauges**

A gauge is simply a value that varies over time. The most recent value of the gauge is the result gost emits
during each flush.

Syntax: `<key>:<value>|g`

Example: `active_users:992|g`

**Sets**

A set records the unique occurrences of some value. The metric sent to graphite is the number of unique values
that were given under a particular key during a flush interval.

Syntax: `<key>:<value>|s`

Example: `user_id:135|s`

### Meta-stats

Gost sends back some stats about itself to graphite as well. This includes:

* `gost.bad_messages_seen`: a counter for the number of malformed messages gost has received
* `gost.packets_received`: a counter for the number of packets gost has read
* `gost.distinct_metrics_flushed`: a gauge for the number of stats sent to graphite during this flush
* `gost.distinct_forwarded_metrics_flushed`: a gauge for the number of stats forwarded to another gost during
  this flush (see Counter Forwarding, below)

There are some other counters for various error conditions. Most of these also show up in the stdout of gost
if you use the `debug_logging = true` option in the configuration.

### OS stats

One nice feature of gost is that, if you're running on a Linux system, it can automatically send back
statistics about the host, including memory, CPU, network, and disk information. See [the example
configuration file](conf.toml) for how to set this up, and detailed information on what counters are sent.

### Script stats

Gost is able to consume messages via scripts that emit statsd-formatted messages to stdout. See [the
configuration file](conf.toml) for the options to specify the script directory and the interval between runs.

Each run interval, gost tries to list the script directory. For each regular file in that directory, gost
tries to run it as an executable (all at the same time). The output is read line by line and each is parsed as
a statsd message.

If one line is unable to be parsed, gost stops trying to parse the output of that script. If the execution
takes so long that the next run interval passes, that script is not started again until it is finished (so at
most one copy of each script is running at once).

The scripts are executed with no arguments from gost's current directory. Stdin and stderr are null devices.
Only stdout is used. Scripts must be executable. Any errors running the script (including a non-zero exit
status) trigger debugging output and meta-stats.

### Debug interface

The `debug_port` setting controls the port of a local server that gost starts up for debugging. Gost will
print its (UDP) input and (Graphite) output via TCP to any client that connects to this port. So if you're
using `debug_port = 8126` as in the example config, then you can connect like this:

    $ telnet localhost 8126

and you will see gost's input and output. This is very handy for debugging. You may want to filter out just a
subset of the data; for instance:

    $ nc localhost 8127 | grep '\[out\]' # just outbound messages

## Key Format

Gost message keys are formed from printable ascii characters with a few restrictions, listed below. The
maximum size of an accepted UDP packet (which usually contains one message but may contain several separated
by `\n`) is 10Kb; this sets the only limit on key length.

source char |             converted to              | reason
------------|---------------------------------------|-------
   newline  |                 error                 | newlines end gost messages
    `:`     |                 error                 | colons end gost keys
   space    |                  `_`                  | graphite uses space in its message format
    `/`     |                  `-`                  | graphite can't handle `/` (keys are filenames)
  `<`, `>`  |                removed                | graphite doesn't handle `<` (`>` excluded for symmetry)
    `*`     |                removed                | graphite uses `*` as a wildcard
  `[`, `]`  |                removed                | graphite uses `[...]` for char set matching
  `{`, `}`, |                removed                | graphite uses `{...}` for matching multiple items

Additionally, note that a trailing `.` on a key will be ignored by Graphite, so `foo.` is the same as `foo`.

## Counter Forwarding

Instead of sending to graphite, gost can forward metrics (counters only) to another gost, which in turn sends
to graphite.

Enable forwarding by setting the `forwarding_addr` option to the network address of the gost to which to
forward. Then to forward a counter, prefix it with `f|`:

    f|web.requests:1|c

This counter will not be flushed to graphite, but will be sent to the forwarder gost.

To enable gost to act as a forwarder (that is, it will accept forwarded messages in addition to normal UDP
messages), set the `forwarder_listen_addr` to the bind address to use to listen for forwarded messages. You
can also use the `forwarded_namespace` setting to control the namespace applied to forwarded stats.

**Motivation:** It's inconvenient to always have to sum your graphite queries across all your servers -- often
you only care about the global count. But graphite doesn't add together counters for you when it ingests them.
To get around this, some folks run a network topology where they forward all their metrics into a single
statsd across the network. This has some big downsides:

* It's lossy (UDP)
* The QPS is really limited in a setup like this
* It's a lot of network traffic

With counter forwarding, you can get a lot of the advantages without the disadvantages:

* Gost-to-gost forwarding is over TCP
* Gosts only flush once every N milliseconds, so the raw stats don't cross the network
* The forwarding protocol is an efficient binary format
* You can handle a large volume of metrics this way

Of course, this is still a single point of failure in your metrics collection system, but if you're using
Graphite you've probably got that anyway.

I suggest you host your forwarder gost instance alongside Graphite.

It is possible for a gost instance to forward counters to itself.

## Tuning

If you're trying to push a lot of stats into gost, it may start dropping messages. This may be because your OS
is using a very limited amount of buffer for the UDP socket. You can typically tune this; on my linux system,
for instance, I can bump the limits by using sysctl:

```
# 25MiB read and write buffer sizes
net.core.rmem_max=26214400
net.core.rmem_default=26214400
net.core.wmem_max=26214400
net.core.wmem_default=26214400
```

With such of tuning your gost instance should be able to easily handle hundreds of thousands of messages per
second on moderate hardware.

(This will, of course, incur a lot of system load and typically you'll want to use sampling to limit the gost
qps to something reasonable.)

## Differences with StatsD

* Statsd only allows keys matching `/^[a-zA-Z0-9\-_\.]+$/`; gost is more permissive (see Key Format, above).
* Gauges cannot be deltas; they must be absolute values.
* Timers don't return as much information as in statsd, and they're not customizable.
* gost can record OS stats from the host and deliver them to graphite as well.
* The "meta-stats" gost sends back are different from StatsD (there are a lot fewer of them)
* Gost is very fast. It can handle several times the load statsd can before dropping messages. In my
  unscientific tests on my Linux dev machine, I got statsd up to about 80k qps before it started dropping
  messages, while gost got to 350k+ qps without dropping any messages.
