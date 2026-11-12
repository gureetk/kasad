# kasad

Offline control for TP-Link Kasa smart plugs that speak the local protocol on
TCP port 9999. kasad talks to the plug directly over the LAN, with no cloud
account and no internet connection.

## Status

Early. What works today:

- show a plug's state and clock
- switch the relay on and off
- set the plug's clock from the machine kasad runs on
- run a weekly on/off schedule, as a long-running service

Planned: a JSON API and a small web interface served by the same binary.

## Build

Go 1.22 or newer. There are no third-party dependencies.

```
go build ./cmd/kasad
```

## One-off commands

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

## Running a schedule

```
kasad -host 192.0.2.10 -data /var/lib/kasad serve
```

`serve` reads `schedule.json` from the data directory and switches the plug
when each rule falls due. It runs until it is interrupted, and logs what it
does to standard error.

```json
{
  "rules": [
    {
      "id": "evening-on",
      "name": "Lamp on in the evening",
      "days": ["mon", "tue", "wed", "thu", "fri", "sat", "sun"],
      "time": "18:30",
      "action": "on"
    },
    {
      "id": "night-off",
      "days": ["mon", "tue", "wed", "thu", "fri", "sat", "sun"],
      "time": "22:15",
      "action": "off"
    },
    {
      "id": "weekend-morning",
      "disabled": true,
      "days": ["sat", "sun"],
      "time": "09:15",
      "action": "on"
    }
  ]
}
```

Each rule needs a unique `id`, the `days` it applies on, a 24-hour `time`, and
an `action` of `on` or `off`. `name` and `disabled` are optional. Times are in
the local timezone of the machine running kasad. The schedule is read once,
at start, so restart `serve` after editing the file.

How `serve` behaves:

- **Missed events.** If rules fell due while `serve` was not running, it
  applies the most recent one when it starts and skips the ones before it,
  since the latest decides what state the plug should be in. Progress is kept
  in `state.json` in the data directory.
- **Changed schedules.** A new or edited schedule takes effect from the moment
  `serve` starts. Rules are never applied retroactively.
- **Failures.** If the plug cannot be reached, `serve` retries every 30
  seconds until it succeeds or a later rule takes over.
- **Clock changes.** When clocks go forward, a rule in the skipped hour fires
  an hour later by the new clock. When clocks go back, a rule in the repeated
  hour fires once, the first time. `serve` checks the time at least once a
  minute, so it also copes with the machine's clock being corrected.
- **Manual switching.** `serve` only acts when a rule falls due. Switching the
  plug by hand in between is left alone until the next rule.

`contrib/kasad.service` is a sample systemd unit.

### The plug's clock

These plugs are reported to lose their clock when they lose power, and they
normally set it again from the internet. A plug that is kept off the internet
therefore needs its clock set from the LAN.

kasad's own schedule does not depend on the plug's clock, but schedules stored
on the plug itself do. `serve` sets the plug's clock when it starts and then
every hour. `-sync-interval` changes how often, and `-sync-interval 0` turns
it off. Without `serve`, the `sync` command does the same job from cron:

```
0 * * * * kasad -host 192.0.2.10 sync
```

The plug keeps local time with no UTC offset. `state`, `sync` and `serve`
assume the plug is set to the same timezone as the machine running kasad.

## Layout

- `protocol`: the wire protocol, which is JSON obfuscated with an autokey XOR
  cipher and sent with a 4-byte length prefix.
- `plug`: typed commands built on `protocol`.
- `schedule`: rules, the calculation of when they fall due, and the loop that
  applies them.
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
