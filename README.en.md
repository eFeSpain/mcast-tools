# mcast-tools

[Español](README.md) · **English**

[![CI](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/eFeSpain/mcast-tools/actions/workflows/ci.yml)

Two command-line tools for operating UDP multicast in video networks. Static
binaries with no runtime dependencies, N channels per process and hot reload on
SIGHUP.

| | |
|---|---|
| **[`mcast-dup`](#mcast-dup)** | Duplicates: joins one group and forwards every datagram as-is to one or more different groups, without re-encoding. |
| **[`mcast-send`](#mcast-send)** | Emits: sends a file, the output of an external command, standard input or a generated pattern, with optional RTP and paced by the stream's own PCR clock. |

```
                    ┌──►  239.255.0.1:1234
file.ts             │
   │                │
   ▼                │
mcast-send ──► 239.0.10.1:5000 ──► mcast-dup ──┤
                                               │
                                               └──►  239.255.1.1:1234
```

They share `internal/mcast`: configuration, interface resolution, sockets,
channel orchestration, statistics and messages. A fix in the shared layer
reaches both, but each binary is installed and run separately.

---

<a name="mcast-dup"></a>

## mcast-dup

Joins one group and forwards every datagram as-is to one or more different
groups. No re-encoding, no touching the payload.

```
239.0.10.1:5000  ──►  mcast-dup  ──┬──►  239.255.0.1:1234
                                   └──►  239.255.1.1:1234
```

## What it is for

Re-addressing streams between different multicast plans: the encoder emits on
`239.0.10.x` and the consumer expects `239.255.x.x`, or the same channel has to
reach two destinations at once, or you need a copy on a range that a particular
router will actually forward. Everyday work in IPTV operations, SDI-over-IP and
in-house signal distribution.

## What it is not

| If what you want is… | Use |
|---|---|
| repeating the **same** group on another interface or VLAN | [udp-broadcast-relay-redux](https://github.com/udp-redux/udp-broadcast-relay-redux), [alsmith/multicast-relay](https://github.com/alsmith/multicast-relay) |
| serving multicast over HTTP to unicast clients | [udpxy](https://github.com/tydaikho/udpxy) |
| routing multicast between interfaces (PIM/IGMP) | smcroute, igmpproxy |
| moving a single stream from a shell | `multicat` (VideoLAN), `socat` |

`mcast-dup` **rewrites the destination group**, which none of those do, and
handles many channels at once, reloading without cutting the ones that did not
change.

## Building

```sh
go mod download
CGO_ENABLED=0 go build -o mcast-dup  ./cmd/mcast-dup
CGO_ENABLED=0 go build -o mcast-send ./cmd/mcast-send

# cross-compiled, for a Linux box
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mcast-dup ./cmd/mcast-dup
```

## Usage

### One channel, from the command line

```sh
mcast-dup -s 239.0.10.1:5000 -d 239.255.0.1:1234,239.255.1.1:1234 \
          -iface 10.30.0.5 -ttl 8
```

| Option | Default | What it does |
|---|---|---|
| `-s` | — | source `GROUP:PORT` (required) |
| `-d` | — | destination(s) `GROUP:PORT`, comma-separated (required) |
| `-iface` | auto | NIC to receive and send on: local IP (`10.30.0.5`) or name (`eth0`) |
| `-ttl` | 8 | outgoing multicast TTL |
| `-loop` | true | local multicast loopback |
| `-rcvbuf` | 4 MiB | `SO_RCVBUF` on the receiving socket |
| `-sndbuf` | 0 | `SO_SNDBUF` (0 = leave it alone) |
| `-stats` | 10 | seconds between summaries (0 turns them off) |
| `-watchdog` | 60 | seconds without a single datagram before rebuilding the socket (0 turns it off) |
| `-logfile` | — | write logs to a file instead of stdout/stderr |
| `-lang` | auto | message language: `auto`, `en` or `es` |

### Message language

Messages, errors and help text follow the system language: **Spanish if the
system is in Spanish, English otherwise**. On Linux and macOS `LC_ALL`,
`LC_MESSAGES` and `LANG` are read in that order; on Windows the user's UI
language is queried.

systemd does not propagate `LANG`, so logs will come out in English there.

### Several channels, daemon mode

```sh
mcast-dup -config /etc/mcast-dup.json
kill -HUP $(pidof mcast-dup)      # reload without cutting the untouched channels
```

See [`mcast-dup.example.json`](mcast-dup.example.json).

## Configuration

`defaults` applies to every channel, and each channel can override whatever it
needs.

| Field | Scope | Default | What it does |
|---|---|---|---|
| `iface` | both | auto | NIC for rx and tx: local IP or interface name |
| `ttl` | both | 8 | outgoing multicast TTL |
| `loop` | both | true | local multicast loopback |
| `rcvbuf` | both | 4194304 | `SO_RCVBUF` in bytes |
| `sndbuf` | both | 0 | `SO_SNDBUF` in bytes (0 = leave it alone) |
| `stats` | defaults | 10 | seconds between summaries (0 turns them off) |
| `watchdog` | both | 60 | seconds without a single datagram before rebuilding the socket (0 turns it off) |
| `name` | channel | its `source` | name in the logs; identifies the channel across reloads |
| `source` | channel | required | source `GROUP:PORT`, must be multicast |
| `dest` | channel | required | list of destination `GROUP:PORT` (unicast works too) |
| `from` | channel | — | senders the group is accepted from; empty accepts any. See [Filtering by sender (SSM)](#filtering-by-sender-ssm) |
| `analyze` | both | true | inspect the transport stream going through. See [What the stream carries, and whether it is healthy](#what-the-stream-carries-and-whether-it-is-healthy) |

### Filtering by sender (SSM)

With `from` on a channel, the group is only accepted from those senders:

```json
{ "name": "la1-hd", "source": "232.1.2.3:5000", "dest": ["239.255.0.1:1234"],
  "from": ["10.20.30.40"] }
```

This happens in two layers. First a **source-specific join** is attempted (SSM,
RFC 4607): the kernel filters, and with IGMPv3 in the network the switch does
not even send us other senders' traffic. And it is **always checked here** too,
in the receive loop, because if the network does not speak IGMPv3 the kernel may
deliver other senders' traffic anyway.

This closes a hole the destination filter does not cover: that one blocks what
is not addressed to the group, but does nothing against the **wrong sender
emitting to the right group** — the misconfigured backup encoder emitting in
parallel, and the decoder showing continuity errors nobody can explain.

On Windows `x/net` does not implement the source-specific join: there it joins
the whole group and filters in userspace, logging that it is not stopping the
traffic upstream.

### What gets validated, at startup and on every reload

- The source has to be a multicast address.
- The addresses in `from` have to be unicast: a group or a `0.0.0.0` there
  filters nothing.
- `ttl` has to be within `[0,255]`. Out of range the kernel rejects the option
  and the socket silently stays at TTL 1, so the channel is rejected instead of
  quietly emitting where it should not.
- Any channel that **creates a feedback loop** is rejected: a destination that
  comes back, directly or indirectly, to the channel's own source. With
  loopback enabled that multiplies the stream on every lap until the NIC is
  saturated. That includes the loop that sneaks in through the unicast door: a
  destination like `127.0.0.1:5000` or `<this-host's-IP>:5000`, when 5000 is
  some channel's source port, re-enters through the receiving socket just the
  same. A legitimate cascade (channel A feeds the group channel B reads) is
  allowed.
- Addresses must carry a port. A `239.0.10.1:0` used to be accepted and then
  bound to an ephemeral port: the channel joined the group and never received a
  single datagram, while looking perfectly healthy.
- An absurd `stats` value is clamped to 24 h instead of overflowing the timer.
- Destinations repeated within a channel are dropped with a warning: they would
  send every packet twice to the same place.
- Channels with a duplicate name or invalid addresses are dropped with a
  warning, without taking the others down. A channel with no `name` gets its
  `source` as the name: a name derived from its position in the array would
  mean that inserting a channel renames the following ones and SIGHUP restarts
  them all.
- Fields the program does not know about are warned about but are **not**
  fatal: a misspelled `"defualts"` would silently lose the whole block, and now
  it shows up in the log; but a `"_comment"` key (JSON has no comments) does not
  take the configuration down.

In flags mode any problem is fatal and the process exits with status 2. In
daemon mode the offending channel is skipped and the rest carries on.

### Reloading

`SIGHUP` starts new channels, stops the ones that disappeared and restarts only
the ones that changed, waiting for the old channel to release its sockets
before starting the replacement.

SIGHUP also does what that signal conventionally means: it **reopens the
`-logfile`**. Without that, after a `logrotate` the process would keep writing
to the old inode until the next restart. In flags mode there is nothing to
reload, so SIGHUP only reopens the log and says so — it used to kill the
process.

Nothing that is already on air is cut by an editing mistake:

- Invalid JSON takes nothing down: it is logged and the previous configuration
  stays up.
- A channel that **is still in the file but whose new configuration fails to
  validate** keeps running exactly as it was, with a warning in the log. Only
  channels that disappear from the file are stopped, which is a deliberate
  decision.
- With one exception: if keeping it would **clash with the channels that did
  validate**, it is stopped and the reason is logged. Keeping it blindly would
  reintroduce through the back door exactly what validation just rejected — it
  only takes swapping two destinations and mistyping the second channel to end
  up with a feedback loop in the relay, or two sender channels aimed at the
  same group.

### Reading the statistics

```
[13:54:42] la1-hd              950 pkt/s · rx   9.98 Mbps · tx  29.94 Mbps · 0 err · 3 drop
```

`rx` is what comes in and `tx` what actually goes out, measured: with three
destinations the link carries three times what is received, and `tx` is the
number to compare against the link's capacity.

`err` and `drop` are **counts for the interval, not rates**, on purpose: they
are rare events and as a rate they would round to zero — one error every ten
seconds is `0.1 err/s`, which prints as `0` and you never see it. `err` are send
failures; `drop` are datagrams discarded for not being addressed to the
channel's group.

Whenever there is an `err`, the next log line carries the actual reason for the
last failure (`network is unreachable` and friends), not just the counter.

### What the stream carries, and whether it is healthy

The statistics tell you **how much** goes through. They do not tell you **what**
goes through, and those are different questions: `err` is send failures and
`drop` is datagrams for another group, so **a transport stream can be broken
with both counters at zero**. Continuity errors, a PCR that jumps, a PAT that
disappears — none of it moves a single figure on the usual line.

That leaves the three-in-the-morning question unanswered: the customer says the
channel is stuttering, you look at the log and it says `0 err · 0 drop`. Now
what?

`mcast-dup` looks at the TS it relays. When the channel starts, once the PMT
goes by, it says what is inside — once:

```
[13:54:32] la1-hd  carries programme 1: PCR on PID 101 · 101 H.264 · 102 AAC · 103 AC-3
```

And on each summary it adds a line **only when there is something to say**:

```
[13:54:42] la1-hd     950 pkt/s · rx 9.98 Mbps · tx 29.94 Mbps · 0 err · 0 drop
[13:54:42] la1-hd  TS: 47 continuity errors (worst PID 101) · 3 PCR jumps with no discontinuity_indicator
```

With that, the answer stops being a shrug: **the problem comes from upstream and
you have the PID**. It is the difference between "my relay is fine" and "the
signal is fine", which is exactly what you have to prove when you sit in the
middle of the chain and look guilty by proximity.

A healthy channel **prints nothing**, deliberately: a log that says "all good"
every ten seconds is a log nobody reads, and then nobody reads the one that
says something either.

What is checked — the subset of ETSI TR 101 290 Priorities 1 and 2 you can look
at without decoding anything:

| | |
|---|---|
| **Continuity** | errors per PID, naming the worst. It honours what the standard allows: the counter only advances on packets with payload, one exact duplicate is legitimate, and a declared discontinuity is not a fault |
| **PCR** | intervals above 40 ms, jumps with no `discontinuity_indicator`, and a missing 27 MHz extension |
| **PAT** | intervals above 500 ms, measured on the stream's own clock |
| **Integrity** | packets with `transport_error_indicator`, datagrams that are not TS, and datagrams that do not carry whole 188-byte packets |
| **Scrambling** | packets with `scrambling_control` set |

It understands both raw TS and TS inside RTP.

It is **on by default**, and the cost is measured: **192 ns per 1316-byte
datagram** with the tables already parsed, which at 10 Mbps is 950 datagrams per
second per channel — 0.018 % of one core. Analysis happens *after* forwarding, never before. It can still be
turned off with `"analyze": false`, in `defaults` or per channel: in a relay,
anything that is not forwarding is optional on principle.

What it does **not** do: it does not reassemble PSI sections split across
packets (not needed for ordinary PAT and PMT), it does not verify table CRCs,
and it does not read the SDT, so no service names. And it is no substitute for a
certified analyser: if you need formal certification, that is TSDuck or a
bench instrument.

### Receive watchdog

If the NIC is recreated with a different ifindex or changes its IP, the group
membership is left orphaned and the socket never receives anything again —
forever, and silently. After `watchdog` seconds without a single datagram the
channel rebuilds the socket, resolves the interface again and redoes the join.
The warning is logged **once per episode**, not on every retry, so a channel
that is legitimately off overnight does not flood the log.

Set it to 0 if you have channels that are legitimately quiet for hours.

---

<a name="mcast-send"></a>

## mcast-send

Emits a multicast stream, raw UDP or encapsulated in RTP, at the bitrate you ask
for or at whatever the stream's own clock dictates. Four possible sources:

```sh
# a TS file, looped
mcast-send -d 239.0.10.1:5000 -f bars.ts -b 10M -iface eth0

# any other format: ffmpeg remuxes it and mcast-send supervises it
mcast-send -d 239.0.10.1:5000 -b 8M \
           -exec "ffmpeg -v quiet -re -i input.mp4 -c copy -f mpegts -muxrate 8000000 -"

# whatever arrives on standard input
ffmpeg -re -i input.mp4 -c copy -f mpegts - | mcast-send -d 239.0.10.1:5000 -stdin -b 8M

# a numbered pattern, no material needed
mcast-send -d 239.0.99.1:5000 -b 2M
```

| Option | Default | What it does |
|---|---|---|
| `-d` | — | destination `GROUP:PORT` (required) |
| `-f` | — | file to emit |
| `-stdin` | false | read from standard input |
| `-exec` | — | run this command and emit its standard output |
| `-rtp` | false | wrap in RTP (RFC 3550, PT 33) |
| `-b` | 10M | bitrate: bits/s, with a suffix (`10M`, `512k`) or `pcr` |
| `-size` | 1316 | payload bytes per datagram (1316 = 7 TS packets) |
| `-loop-file` | true | restart the file when it ends |
| `-iface`, `-ttl`, `-loop`, `-sndbuf`, `-stats`, `-logfile`, `-lang` | | as in `mcast-dup` |

With none of `-f`, `-exec` or `-stdin` it emits a **numbered pattern**: each datagram
carries its sequence number in the first 8 bytes, so at the other end you can
check that none is missing, none is repeated and they arrive in order. That is
what the repository's automated test uses.

### When it finishes, and what it reports while emitting

With `-loop-file=false`, or when the `-stdin` pipe closes, the channel has done
its job: it is logged as finished and, if no other channel is still emitting,
**the process exits with status 0**. It does not hang around or reopen the
source, which is what it would do if it confused "I am done" with "I crashed".

The sender's summary does not carry the relay's columns, because it receives
nothing:

```
[13:54:42] bars                950 pkt/s · tx   9.98 Mbps · 0 err · 0 rebase
```

`rebase` counts how many times the clock had to be **rebased**: the anchor moved
to the present instead of trying to make up the lag all at once. It happens when
the source is not keeping up with the requested bitrate — a slow disk, ffmpeg
taking a while to start, a bitrate configured above what the material can give —
and also when the stream's PCR jumps.

Catching up the hard way would be worse than the lag itself: it is the backlog
going out at wire speed, exactly what blows the decoder's buffer. Hence the
rebase.

One `rebase` **per episode** is normal. A counter that keeps climbing means you
are asking for more than your source can deliver; the stream still goes out, but
not at the rate you think. The next log line carries the last one's offset.

### Other formats: `exec`

UDP multicast carries MPEG-TS. An MP4, an MKV or a MOV **cannot be emitted
as-is**: they would go out at their bitrate, with zero errors in the
statistics, and no decoder could read them. So if you hand one over, it is
rejected with the exact command to fix it:

```
channel 'movie': /srv/movie.mp4 is MP4/MOV, and UDP multicast carries MPEG-TS.
                 Remux it without re-encoding:
                 ffmpeg -i /srv/movie.mp4 -c copy -f mpegts /srv/movie.ts
```

And so you do not have to prepare the material by hand, `exec` runs that
conversion **under supervision**:

```json
{ "name": "movie", "dest": "239.0.10.3:5000", "bitrate": "6M",
  "exec": "ffmpeg -v quiet -re -i /srv/media/movie.mp4 -c copy -f mpegts -" }
```

The command lives and dies with the channel: if it crashes, the channel goes
down with it and is retried like any other; if you stop the channel, the command
is killed — verified that no orphan processes are left behind. And when it dies,
the log carries **its last error output**, which is what you need to know what
happened:

```
[movie] relay down (payload: the source command failed (exit status 1);
        its last output: No such file or directory); retrying in 3s
```

Each channel spawns its own, so this **does scale to multi-channel**, unlike
`-stdin`, which only one channel per process can read from.

The command does not go through a shell: it is split honouring quotes and
executed directly. No `*` expansion, no pipes, no `&&` — and no injection from
the config file either. What whoever writes the JSON *can* do is run any binary
they like: `exec` hands over process control, so the config file deserves the
same permissions as the service itself.

#### The ffmpeg command you actually need for broadcast

`-c copy -f mpegts` fixes the **format**, which is what makes the stream
readable. It leaves the **timing** half done, and that does not show until you
measure it. Measured over 20 s of H.264 25 fps with a 2 s GOP plus AAC:

| | `-c copy -f mpegts` | adding `-muxrate 6000000` | Reference |
|---|---|---|---|
| PCR repetition | 80.0 ms — **100 % out** | 20.0 ms — 0 % out | ≤ 40 ms |
| PCR 27 MHz extension | **0 across all 250 samples** | present in 960 of 1004 | indispensable |
| Rate between PCRs | 4.06 – 8.33 Mbps → **swings 2.1×** | 6.00 – 6.00 Mbps → 1.0× | constant |
| Null packets | 0 | 8716 (10.9 %) | as many as needed |

The second row is the one that decides it: **without `-muxrate`, ffmpeg does
not compute the PCR's 27 MHz extension either**. Without it the clock is
quantised to 90 kHz — 11.1 µs — twenty-two times above the PCR_accuracy
tolerance TR 101 290 asks for. With `-muxrate` it computes it. Not sufficient
for conformance, but impossible without.

So the command for broadcast is:

```sh
ffmpeg -v quiet -re -i movie.mp4 -c copy -f mpegts \
       -muxrate 6000000 -mpegts_flags +system_b -
```

And little else is needed: `-pat_period` (0.1 s) and `-sdt_period` (0.5 s)
already ship with sane defaults, and `-pcr_period 20` is redundant — with
`-muxrate` the output is byte-for-byte identical with and without it.
`+system_b` is DVB signalling instead of the ATSC default.

**The one delicate parameter is `-muxrate` itself, and undershooting is worse
than omitting it.** Asking 4M of that same 5.36 Mbps stream makes ffmpeg warn
`dts < pcr, TS is invalid` and stretch the timeline: 20 s of material came out
spread over 26.9 s. The treacherous part is that the conformance metrics look
impeccable — PCR every 19.9 ms, rate nailed to 4.00 Mbps — with the stream
broken. Set it comfortably above the sum of the elementary streams; whatever is
left over gets filled with null packets.

With the output already CBR, `"bitrate": "pcr"` does what it promises: the pace
comes from the multiplexer's clock rather than an estimate of yours.

### Let the stream set the pace: `"bitrate": "pcr"`

Getting the bitrate right by hand is a real problem: ask for 10 Mbps from a TS
that is really 6 and you emit it 1.6× too fast and blow the decoder's buffer;
undershoot and you starve it. And with variable-bitrate material there is no
correct number.

The transport stream itself carries the answer. The **PCR** (*Program Clock
Reference*) is the muxer's clock, in 27 MHz units, travelling in the adaptation
field of some packets. With `pcr` instead of a number, the sender reads it and
replays the stream **exactly at the rate it was created**:

```json
{ "name": "movie", "dest": "239.0.10.3:5000", "bitrate": "pcr" }
```

Measured on a TS whose PCR says 6 Mbps:

| Configuration | Emitted |
|---|---|
| `"bitrate": "20M"` | 12.85 Mbps — whatever you say, even when wrong |
| `"bitrate": "pcr"` | **6.00 Mbps** — the stream's real rate |

It anchors to the stream's clock and interpolates between PCRs using the rate
measured between the last two, so it does not accumulate drift however long the
emission lasts. A counter wrap — every ~26 h — a material splice, or a
discontinuity the stream declares itself is detected and re-anchored, instead of
trying to catch up all at once with a burst.

That last part matters more than it sounds, and it has a test: the suite stalls
the source for 1.5 s in the middle of a 6 Mbps emission and measures the **peak**
over 200 ms windows. If only the fixed-bitrate clock were re-anchored on resume
and not the PCR one, the target would still be computed by the pacer against its
old anchor, every turn of the loop would see itself late again, and it would
never sleep:

| | Peak | `rebase` |
|---|---|---|
| re-anchoring only the fixed-bitrate clock | 38.8 Mbps | 599 |
| re-anchoring both | **6.2 Mbps** | **1** |

If the stream carries no PCR (not TS, or none present), after 4 MB it warns and
falls back to the configured bitrate, which is still needed as the initial
estimate: there is no measured rate until the second PCR. That estimate comes
from `defaults`, so a channel with `"bitrate": "pcr"` wants a number in
`defaults` in the right ballpark — bootstrapping a 50 Mbps stream at 10 leaves
the first few milliseconds in slow motion.

It requires `size` to be a multiple of 188, otherwise the TS packets come out
split and there is no PCR to read.

### RTP: `"rtp": true`

Adds RFC 3550's 12-byte header with *payload type* 33 (MP2T). SMPTE 2022-2 and
many professional decoders need it, and it also gives the receiver **sequence
numbers** to detect loss and reordering, which is impossible with raw UDP.

That is why the default payload is 1316 bytes: 1316 + 12 = 1328, comfortably
inside a 1500 MTU. Sequence and SSRC start random, as the standard requires.

If the datagram goes past the 1472 bytes that fit in that MTU — RTP's 12
included — it warns but does not reject: on a jumbo-frame network it is
legitimate. What is not legitimate is not knowing, because IP fragments and
**losing one fragment loses the whole datagram**; and some routers do not
reassemble fragmented multicast at all. It is the kind of loss that costs weeks
to diagnose later.

### It is still not a muxer

`mcast-send` chunks, paces and encapsulates. Of the transport stream it reads
only the adaptation field — just enough to find the PCR; it **does not parse the
content**: it does not touch PSI/SI tables, does not rewrite PIDs or service
IDs, does not re-encode and does not multiplex several programmes into one.

What it does **not** do, and what you need instead:

| | |
|---|---|
| FEC (SMPTE 2022-1) or seamless protection (2022-7) | a dedicated gateway |
| SRT, RIST, Zixi | `srt-live-transmit`, `ristsender` |
| transcode, remux, rewrite PSI/SI | `ffmpeg`, `tsduck` — `exec` puts them right there |
| IPv6, RTCP, retransmission | — |

With `exec` it delegates the format to something that knows how. It brings the
clock, the RTP header and the socket.

The default size is **1316 = 7 × 188**, a whole number of TS
packets. If the material looks like a transport stream — checked by looking for
the `0x47` sync bytes — and the alignment does not add up, it says so:

```
config: channel 'bars': /srv/bars.ts looks like MPEG-TS, but the datagram size
        (1400) is not a multiple of 188: TS packets would be split across
        datagrams and no decoder could read the stream
```

Same for the **file's length** not being a multiple of 188: every loop would
emit a truncated packet. These are warnings and not rejections, because emitting
something that is not TS at whatever size you like is a legitimate use — and
that is exactly why the warning only shows up when the file really does look
like TS.

It is the most treacherous class of bug: the stream goes out at its bitrate,
with zero errors in the statistics, and simply no decoder can read it.

### Daemon mode works exactly like the relay's

```sh
mcast-send -config /etc/mcast-send.json
systemctl reload mcast-send
```

See [`mcast-send.example.json`](mcast-send.example.json). Same rules:
`defaults` plus per-channel overrides, reloads that do not cut what did not
change, a channel whose new config fails to validate stays as it was, and
up-front validation of what cannot work (destination without a port, unreadable
bitrate, missing file, two channels aimed at the same destination).

| Field | Scope | Default | What it does |
|---|---|---|---|
| `name` | channel | its `dest` | name in the logs; identifies the channel across reloads |
| `dest` | channel | required | `GROUP:PORT` to emit to |
| `file` | channel | — | file to emit |
| `loop` | channel | true | restart the file when it ends |
| `stdin` | channel | false | read from standard input |
| `exec` | channel | — | external command whose standard output is emitted |
| `rtp` | both | false | wrap each datagram in RTP |
| `bitrate` | both | `10M` | bits/s, with a suffix, or `pcr` to follow the stream clock |
| `size` | both | 1316 | payload bytes per datagram |
| `stats` | defaults | 10 | seconds between summaries (0 turns them off) |
| `iface`, `ttl`, `loopback`, `sndbuf` | both | | as in the relay |

### Why emitting is harder than forwarding

The relay is **paced by its input**: it forwards when something arrives, and the
clock is the original sender's. An emitter has to **own its clock**, and that is
where all the difficulty is.

A TS at 10 Mbps with a 1316-byte payload is ~950 packets/s: one every 1.05 ms.
Sleeping "one interval" per iteration does not work, because each sleep's drift
accumulates and an hour later you are minutes off. `mcast-send` uses an
**absolute clock**: it computes when packet N should go out counting from
startup, so one sleep's error corrects itself on the next iteration.

Measured with the real binaries: 4 Mbps requested, 380 pkt/s × 1316 B =
**4.00 Mbps** emitted, with the relay on the other side reporting
`rx 4.00 · tx 4.00`.

---

## systemd

```sh
install -m 0755 mcast-dup mcast-send /usr/local/bin/
install -m 0644 mcast-dup.example.json  /etc/mcast-dup.json
install -m 0644 mcast-send.example.json /etc/mcast-send.json
install -m 0644 mcast-dup.service mcast-send.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mcast-dup
systemctl reload mcast-dup        # after editing /etc/mcast-dup.json
```

Each tool has its own unit, config file and lifecycle: a box that only relays
does not need the emitter installed.

## Network tuning

`rcvbuf` and `sndbuf` are clamped by the kernel to `net.core.rmem_max` /
`net.core.wmem_max`. So that 4 MiB really is 4 MiB:

```sh
sysctl -w net.core.rmem_max=8388608
sysctl -w net.core.wmem_max=8388608
```

If you see drops, that is the first place to look, together with
`netstat -su | grep -i 'receive errors'`.

## The detail almost every multicast relay gets wrong

If two channels share the source port with different groups
(`239.0.10.1:5000` and `239.0.20.1:5000`, entirely normal in IPTV), on Linux
**each socket also receives the other one's traffic** and forwards it to its
own destination: the stream comes out crossed and duplicated.

The cause is `IP_MULTICAST_ALL`, which defaults to 1: a wildcard-bound socket
receives *every* group joined anywhere on the host that arrives on its port,
not just the ones that socket joined. `mcast-dup` sets it to 0 (see
[`internal/mcast/control_linux.go`](internal/mcast/control_linux.go)).

What does **not** work as an alternative in Go is binding to the group instead
of the wildcard: the `net` package rewrites any multicast bind address to
`0.0.0.0` (`listenDatagram`, in `src/net/sock_posix.go`). That fix compiles,
looks right, and does exactly nothing.

Measured on Linux with two receivers on port 5000 joined to different groups,
sending one packet to group A:

| RX socket options | A receives | B receives |
|---|---|---|
| `SO_REUSEADDR` | yes | **yes** ← crossed |
| `SO_REUSEADDR` + `SO_REUSEPORT` | yes | **yes** ← crossed |
| `SO_REUSEADDR` + `IP_MULTICAST_ALL=0` | yes | no |

Windows and the BSDs need none of this: they deliver to each socket only the
groups that socket joined. Verified on Windows; on BSD it is the documented
behaviour, and precisely what made Linux add the option in the first place.

### And that same wildcard bind leaves another door open

`IP_MULTICAST_ALL=0` fixes the crossing **between groups**, but it does not
touch unicast: a socket on `0.0.0.0:5000` still receives whatever is sent to the
host's own IP on that port, plus broadcast. Without checking the destination
address, the relay would forward that foreign traffic into the multicast group.
Measured: three unicast datagrams sent to `<relay-IP>:5000` came out intact in
the destination group.

That is why the receiving socket asks for each datagram's destination address
(`IP_PKTINFO`, via `SetControlMessage(ipv4.FlagDst, true)`) and discards
anything not addressed to the channel's group. Discards are counted and shown in
the statistics as `drop/s`, so a misaimed `ffmpeg` or a UDP scan shows up in the
log instead of ending up inside the transport stream.

On Windows this is not possible: `x/net/ipv4` does not implement
`SetControlMessage` (it returns `errNotImplemented`), so the filter is disabled
there and startup says so explicitly, per channel.

## Limitations

- **IPv4 only.**
- **The source-specific join (SSM) is not available everywhere.** Filtering by
  sender with `from` always works, but the traffic is only cut off *in the
  network* where the platform implements the source-specific join. On Windows
  `x/net` does not, so there the filtering happens in the relay itself: it
  protects the stream just the same, but the foreign sender's traffic reaches
  the NIC and is discarded afterwards. Startup warns about it.
- **One syscall per packet per destination.** It does not use
  `recvmmsg`/`sendmmsg`. Plenty for dozens of channels; if you need hundreds,
  that is where the ceiling is.
- **Linux is the primary platform.** It builds and runs on Windows and
  macOS/BSD, but Windows has neither `SIGHUP` (no hot reload) nor the ability to
  filter by destination address (startup warns about it).
- **What the tests do not reach.** The data path is covered: the suite brings up
  sender, relay and receiver over real sockets and checks that the numbered
  pattern arrives complete, with no duplicates, no gaps and no mixing with
  another group; that with `rtp` the datagrams carry their 12-byte header, an
  intact payload and an unbroken sequence; and that a stalled source does not
  turn into a burst. What **cannot** be tested in CI is
  **SSM filtering at the network level**: proving that the switch does
  not even send us other senders' traffic needs IGMPv3 in the path, and that
  cannot be set up on a CI runner. What is verified is that the join happens
  and that the foreign sender does not reach the destination.

## License

MIT. See [LICENSE](LICENSE).

The binaries are static, so the executable carries the Go runtime plus
`golang.org/x/net` and `x/sys` inside it — all three BSD 3-Clause. Their
notices are in [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md), which is what
those licenses require when redistributing in binary form; keep it in step with
`go.mod`.
