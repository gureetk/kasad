# kasad

Offline control for TP-Link Kasa smart plugs that speak the local protocol on
TCP port 9999. kasad talks to the plug directly over the LAN, with no cloud
account and no internet connection.

## Status

Early. What works today is a command-line tool that can:

- show a plug's state and clock
- switch the relay on and off
- set the plug's clock from the machine it runs on

Planned: scheduling, and a small web interface served by the same binary.

## Build

Go 1.22 or newer. There are no third-party dependencies.

```
go build ./cmd/kasad
```

## Usage

```
kasad -host 192.0.2.10 state
kasad -host 192.0.2.10 on
kasad -host 192.0.2.10 off
kasad -host 192.0.2.10 sync
```

The address can also come from the environment:

```
export KASAD_HOST=192.0.2.10
kasad state
```

`-host` takes a host name or IP address, optionally with a port
(`192.0.2.10:9999`). `-timeout` sets how long to wait for the plug, and
defaults to 5 seconds.

kasad exits with status 0 on success, 1 if the plug could not be reached or
refused a command, and 2 if the command line was wrong.

### The plug's clock

These plugs are reported to lose their clock when they lose power, and they
normally set it again from the internet. A plug that is kept off the internet
therefore needs its clock set from the LAN, which is what `sync` does. Running
it from cron or a systemd timer keeps the clock right:

```
0 * * * * kasad -host 192.0.2.10 sync
```

The plug keeps local time with no UTC offset. `state` and `sync` assume the
plug is set to the same timezone as the machine running kasad.

## Layout

- `protocol`: the wire protocol, which is JSON obfuscated with an autokey XOR
  cipher and sent with a 4-byte length prefix.
- `plug`: typed commands built on `protocol`.
- `cmd/kasad`: the command-line tool.
- `internal/mockplug`: an in-memory mock plug, so the tests run without
  hardware.

## Security

The protocol has no authentication. Anything that can reach port 9999 on a
plug can control it, so keep plugs on a network that only trusted machines
can reach.

## Tests

```
go test -race ./...
```
