# mcast-dup

[Español](README.md) · **English**

[![CI](https://github.com/eFeSpain/mcast-dup/actions/workflows/ci.yml/badge.svg)](https://github.com/eFeSpain/mcast-dup/actions/workflows/ci.yml)

Duplicates UDP multicast streams: joins one group and forwards every datagram
as-is to one or more different groups. No re-encoding, no touching the payload.

A static binary with no runtime dependencies, N channels in a single process
and hot reload on SIGHUP.

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
CGO_ENABLED=0 go build -o mcast-dup .

# cross-compiled, for a Linux box
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mcast-dup .
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
| `-iface` | auto | local IP of the NIC used to receive and send |
| `-ttl` | 8 | outgoing multicast TTL |
| `-loop` | true | local multicast loopback |
| `-rcvbuf` | 4 MiB | `SO_RCVBUF` on the receiving socket |
| `-sndbuf` | 0 | `SO_SNDBUF` (0 = leave it alone) |
| `-stats` | 10 | seconds between summaries (0 turns them off) |
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
| `iface` | both | auto | local IP of the NIC (rx and tx) |
| `ttl` | both | 8 | outgoing multicast TTL |
| `loop` | both | true | local multicast loopback |
| `rcvbuf` | both | 4194304 | `SO_RCVBUF` in bytes |
| `sndbuf` | both | 0 | `SO_SNDBUF` in bytes (0 = leave it alone) |
| `stats` | defaults | 10 | seconds between summaries (0 turns them off) |
| `name` | channel | `ch1`, `ch2`… | name in the logs; identifies the channel across reloads |
| `source` | channel | required | source `GROUP:PORT`, must be multicast |
| `dest` | channel | required | list of destination `GROUP:PORT` (unicast works too) |

### What gets validated, at startup and on every reload

- The source has to be a multicast address.
- Any channel that **creates a feedback loop** is rejected: a destination that
  comes back, directly or indirectly, to the channel's own source. With
  loopback enabled that multiplies the stream on every lap until the NIC is
  saturated. A legitimate cascade (channel A feeds the group channel B reads)
  is allowed.
- Channels with a duplicate name or invalid addresses are dropped with a
  warning, without taking the others down.

In flags mode any problem is fatal and the process exits with status 2. In
daemon mode the offending channel is skipped and the rest carries on.

### Reloading

`SIGHUP` starts new channels, stops the ones that disappeared and restarts only
the ones that changed, waiting for the old channel to release its sockets
before starting the replacement. Invalid JSON takes nothing down: it is logged
and the previous configuration stays up.

## systemd

```sh
install -m 0755 mcast-dup /usr/local/bin/
install -m 0644 mcast-dup.example.json /etc/mcast-dup.json
install -m 0644 mcast-dup.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now mcast-dup
systemctl reload mcast-dup        # after editing /etc/mcast-dup.json
```

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
[`control_linux.go`](control_linux.go)).

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

## Limitations

- **IPv4 only.**
- **No SSM, no source filtering** (IGMPv3): you cannot ask for "this group, but
  only from sender X".
- **One syscall per packet per destination.** It does not use
  `recvmmsg`/`sendmmsg`. Plenty for dozens of channels; if you need hundreds,
  that is where the ceiling is.
- **Linux is the primary platform.** It builds and runs on Windows and
  macOS/BSD, but Windows has no `SIGHUP`: no hot reload there.
- Automated tests cover validation, per-group filtering, channel shutdown,
  statistics and translations. The data path itself is verified by hand.

## License

MIT. See [LICENSE](LICENSE).
