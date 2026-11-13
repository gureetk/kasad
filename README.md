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
- offer a web app for switching the plug and editing the schedule
- offer a JSON API for the plug and the schedule

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
the local timezone of the machine running kasad. The file is read once, at
start, so restart `serve` after editing it by hand. A schedule changed through
the API takes effect at once and is written back to the file.

How `serve` behaves:

- **Missed events.** If rules fell due while `serve` was not running, it
  applies the most recent one when it starts and skips the ones before it,
  since the latest decides what state the plug should be in. Progress is kept
  in `state.json` in the data directory.
- **Changed schedules.** A new or edited schedule takes effect from the moment
  `serve` sees it: at start for a file edited by hand, straight away for a
  change made through the API. Rules are never applied retroactively.
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

## Web app

`serve` offers a web app when given an address to listen on:

```
kasad -host 192.0.2.10 -data /var/lib/kasad -listen 127.0.0.1:8080 serve
```

Open `http://127.0.0.1:8080/` in a browser. The page shows whether the plug
is on and switches it, shows when the schedule will next act, and adds, edits,
pauses and deletes rules. It lays itself out for a phone or a desktop, and
follows the device's light or dark setting.

Things to know:

- **It is part of the binary.** The page is a few static files compiled into
  kasad. It loads nothing from anywhere else, so it works on a network with no
  internet. It does need kasad: there is nothing for it to show offline.
- **Times are kasad's.** Rules run by the clock of the machine kasad is on,
  and the page shows those times as they are, whatever timezone the browser
  is in.
- **It asks the plug often.** While the page is open and on screen it
  refreshes every 10 seconds, and each refresh asks the plug for its state.
- **Installing it.** The page has what a browser needs to install it as an
  app. Browsers generally only offer that for pages served over HTTPS or from
  `localhost`, so over plain HTTP on a LAN it is an ordinary page that can be
  bookmarked. To install it, put kasad behind something that adds TLS.

## JSON API

The web app works through a JSON API at the same address, which other
programs can use too:

| Request | What it does |
|---|---|
| `GET /api/status` | The plug's state and clock, and the next scheduled event |
| `POST /api/relay` | Switch the plug: send `{"on": true}` or `{"on": false}` |
| `GET /api/schedule` | The schedule, in the same form as `schedule.json` |
| `PUT /api/schedule` | Replace the whole schedule |

```
curl http://127.0.0.1:8080/api/status
curl -X POST -H 'Content-Type: application/json' -d '{"on": true}' http://127.0.0.1:8080/api/relay
curl -X PUT -H 'Content-Type: application/json' --data @schedule.json http://127.0.0.1:8080/api/schedule
```

Responses are JSON. An error has the form `{"error": "..."}` with a fitting
status code: 400 for a request that is wrong, 502 when the plug could not be
reached or did not switch. `GET /api/status` still answers 200 when the plug
is unreachable, with `"reachable": false`, because the rest of the answer
does not depend on the plug.

Requests that change something must be sent as `application/json`.

### Who can use the web app and API

Without `-listen` there is neither. With it, anyone who can reach the address
can switch the plug and change the schedule, so choose the address with care:
`127.0.0.1` keeps them to the machine itself.

To require a token, set `KASAD_TOKEN` in the environment of `serve`. Every
API request must then carry it:

```
curl -H 'Authorization: Bearer the-token' http://192.0.2.20:8080/api/status
```

The web app's own files are still served to anyone, since a browser has no
token to send when it first asks, and they hold nothing secret. The page then
asks for the token before it can show or do anything, and keeps it in that
browser's storage so that it is only asked for once.

Set a token whenever kasad listens on an address other machines can reach.
`serve` logs a warning if it does not have one. Everything is plain HTTP, so
on a network you do not trust, put it behind something that adds TLS.

## Layout

- `protocol`: the wire protocol, which is JSON obfuscated with an autokey XOR
  cipher and sent with a 4-byte length prefix.
- `plug`: typed commands built on `protocol`.
- `schedule`: rules, the calculation of when they fall due, the loop that
  applies them, and the file-backed store they are kept in.
- `internal/api`: the JSON API.
- `internal/web`: the web app. `static` holds the page as it is served, with
  no build step: plain HTML, CSS and JavaScript modules.
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

The web app's schedule and time arithmetic has tests of its own, which need
Node.js 20 or newer and nothing else. Building and running kasad does not
need Node.js.

```
cd internal/web && node --test
```
