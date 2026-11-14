# kasad

Offline control for TP-Link Kasa smart plugs that speak the local protocol on
TCP port 9999. kasad talks to the plug directly over the LAN, with no cloud
account and no internet connection.

The project page, with screenshots of the web app, is at
<https://kasad.gureet.ca/>.

## Status

Early. What works today:

- show a plug's state and clock
- switch the relay on and off
- set the plug's clock from the machine kasad runs on
- run a weekly on/off schedule, as a long-running service
- list, add, change and delete the rules stored on the plug itself
- offer a web app for switching the plug and editing both kinds of rule
- offer a JSON API for the plug and the schedule

## Install

kasad is one binary with nothing to install beside it. There are three ways
to get it. `kasad version` says which version is running.

### A release, run by systemd

Each [release](https://github.com/gureetk/kasad/releases) has an archive for
64-bit Intel and AMD machines (`linux-amd64`), 64-bit ARM (`linux-arm64`) and
32-bit ARM (`linux-armv7` for a Raspberry Pi 2 or later, `linux-armv6` for
the first Pi and the Pi Zero). `SHA256SUMS` lists their checksums.

```
tar -xzf kasad-v1.2.3-linux-arm64.tar.gz
cd kasad-v1.2.3-linux-arm64
sudo install kasad /usr/local/bin/
sudo install -m 600 kasad.env.example /etc/kasad.env
sudo install -m 644 kasad.service /etc/systemd/system/
```

Edit `/etc/kasad.env`: set `KASAD_HOST` to the plug's address, and
`KASAD_LISTEN` and `KASAD_PASSWORD` if you want the web app. Then:

```
sudo systemctl daemon-reload
sudo systemctl enable --now kasad
```

The unit runs kasad as a user of its own and keeps its files in
`/var/lib/kasad`. Logs go to the journal: `journalctl -u kasad`.

### A container

Each release is also an image, for the same kinds of machine:

```
docker run -d --name kasad --restart unless-stopped \
  -e KASAD_HOST=192.0.2.10 \
  -e KASAD_PASSWORD='a phrase of your own' \
  -e TZ=Europe/Lisbon \
  -p 8080:8080 -v kasad-data:/data \
  ghcr.io/gureetk/kasad:latest
```

The web app is then on port 8080 of the machine.

Or with Docker Compose. Put this in a file named `compose.yaml`, with your
own values for the three settings:

```yaml
services:
  kasad:
    image: ghcr.io/gureetk/kasad:latest
    restart: unless-stopped
    environment:
      KASAD_HOST: 192.0.2.10
      KASAD_PASSWORD: a phrase of your own
      TZ: Europe/Lisbon
    ports:
      - "8080:8080"
    volumes:
      - kasad-data:/data

volumes:
  kasad-data:
```

Then, in the same directory:

```
docker compose up -d
docker compose logs -f
```

To move to a newer release later:

```
docker compose pull
docker compose up -d
```

`latest` is the newest release that is not a pre-release. To stay on one
version, or to run a pre-release, name it instead, as in
`ghcr.io/gureetk/kasad:0.1.0`.

`contrib/compose.yaml` is a variant that reads the settings from a
`kasad.env` file beside it, which keeps the password out of the Compose
file.

Things to know:

- **Set `TZ`.** Rules run by local time, and the plug's clock is set to it.
  In a container local time is UTC unless `TZ` names a timezone. kasad logs
  the time it starts by, with its offset from UTC, so the log shows whether
  it has the right one.
- **The image listens on port 8080** inside the container and keeps its files
  in `/data`. Set a password whenever the port is published to other
  machines.
- **The container has to be able to reach the plug** on TCP port 9999. The
  plug sees the address of the machine Docker runs on.
- **It does not run as root.** kasad runs as user 65532. A named volume, as
  above, is writable by it from the start. A directory of the host mounted on
  `/data` has to be made writable by that user.
- The image holds the binary and nothing else, so there is no shell in it to
  `docker exec`.

To build the image from a checkout instead: `docker build -t kasad .`

### From source

Go 1.22 or newer. There are no third-party dependencies.

```
go install github.com/gureetk/kasad/cmd/kasad@latest
```

Or, in a checkout:

```
go build ./cmd/kasad
```

## Settings

Every flag can also be set through the environment, so a deployment can keep
all its settings in one file. A flag on the command line wins.

| Flag | Variable | What it sets |
|---|---|---|
| `-host` | `KASAD_HOST` | The plug's address, as a host or `host:port` |
| `-timeout` | `KASAD_TIMEOUT` | How long to wait for the plug to answer (default `5s`) |
| `-data` | `KASAD_DATA` | `serve`: the directory kasad keeps its files in (default: the current one) |
| `-sync-interval` | `KASAD_SYNC_INTERVAL` | `serve`: how often to set the plug's clock (default `1h`, `0` for never) |
| `-listen` | `KASAD_LISTEN` | `serve`: the address to offer the web app and the JSON API on (default: neither) |
| | `KASAD_PASSWORD` | `serve`: the password people sign in to the web app with |
| | `KASAD_TOKEN` | `serve`: a token for programs that use the API |

The password and the token have no flags, because a command line can be read
by every user of the machine.

Either can be kept in a file of its own instead: set `KASAD_PASSWORD_FILE` or
`KASAD_TOKEN_FILE` to the file's path. That is how Docker secrets and
systemd credentials hand a secret over, and it keeps the secret out of the
process's environment. The line ending at the end of the file is not part of
the secret. `serve` refuses to start if the file cannot be read or is empty,
or if the variable and its `_FILE` are both set.

kasad does not read a settings file itself. systemd does that with
`EnvironmentFile=`, and Docker with `--env-file` or `env_file:`.
`contrib/kasad.env.example` is a file to start from.

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

To run it as a service, see "Install" above.

## Rules stored on the plug

A plug also keeps a schedule of its own, which is where rules made with the
vendor's app end up. The plug runs those rules by itself, by its own clock,
whether or not kasad is running. They are separate from the schedule that
`serve` runs: kasad does not run them, and `serve` does not stop them. If
both hold rules, both act on the plug.

```
kasad -host 192.0.2.10 rules
```

```
ID                                WHEN    DOES  DAYS                 NAME
00000000000000000000000000000001  07:30   on    Mon Tue Wed Thu Fri  morning
00000000000000000000000000000002  sunset  off   Sat Sun              dusk (switched off)
```

To remove one, give its id:

```
kasad -host 192.0.2.10 delete-rule 00000000000000000000000000000001
```

A deleted rule cannot be brought back. kasad asks the plug what rules it has
first, and only ever asks it to delete one of those.

The web app and the API go further than these two commands. They can add a
rule to the plug and change one, for rules of the plain weekly kind: on or
off at a time of day, on chosen days of every week. In the web app each rule
says whether it runs on kasad or on the plug, and changing that moves it.

Which to use for a rule:

| | Run by kasad | Stored on the plug |
|---|---|---|
| Works while kasad is not running | No | Yes |
| Depends on the plug's clock | No | Yes |
| Retried, and caught up after an outage | Yes | No |

How kasad treats the plug's rules:

- **It checks what the plug stored.** After adding or changing a rule, kasad
  reads the plug's rules back and reports a failure if the rule is not there
  as it was sent.
- **It only names rules the plug has listed.** Before changing or deleting a
  rule, kasad asks the plug what rules it has.
- **Moving is add, then delete.** A rule is stored in its new place before it
  is removed from its old one. If the second step fails, the rule is in both
  places, where it can be seen, and never in neither.
- **Other kinds of rule are left as they are.** A rule timed by sunrise or
  sunset, one that runs once, or one with a second action is listed and can
  be deleted, but not changed.
- **The plug's schedule has a switch of its own.** If it is off, none of the
  plug's rules run. kasad reports that, and does not change it.

Two things are worth knowing:

- Adding, changing and deleting have so far only been tried against the mock
  plug that the tests use, not against hardware. The requests follow
  published notes on the protocol for older models of plug.
- The plug gives a rule's days as seven flags. kasad reads and writes them as
  starting on Sunday, which is how other clients for these plugs treat them,
  but that has not been checked against a plug.

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
is on and switches it, shows when a rule will next act, and adds, edits,
pauses and deletes rules. It lists the rules kasad runs and the rules stored
on the plug side by side, and a rule can be moved from one to the other. It
lays itself out for a phone or a desktop, and follows the device's light or
dark setting.

Things to know:

- **It can ask people to sign in.** Start `serve` with `KASAD_PASSWORD` set
  and the page asks for that password first. See "Who can use the web app and
  API" below.
- **It is part of the binary.** The page is a few static files compiled into
  kasad. It loads nothing from anywhere else, so it works on a network with no
  internet. It does need kasad: there is nothing for it to show offline.
- **Times are kasad's.** Rules run by the clock of the machine kasad is on,
  and the page shows those times as they are, whatever timezone the browser
  is in.
- **It asks the plug often.** While the page is open and on screen it
  refreshes every 10 seconds, and each refresh asks the plug for its state.
  It reads the rules stored on the plug once a minute.
- **The chart shows both kinds of rule.** The bars for today and the week,
  and the next event, take in the rules kasad runs and the weekly rules stored
  on the plug.
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
| `GET /api/schedule` | The schedule kasad runs, in the same form as `schedule.json` |
| `PUT /api/schedule` | Replace the whole schedule kasad runs |
| `GET /api/plug/rules` | The rules stored on the plug, which it runs by itself |
| `POST /api/plug/rules` | Add a rule to the plug |
| `PUT /api/plug/rules/{id}` | Change one of the rules stored on the plug |
| `DELETE /api/plug/rules/{id}` | Delete one of the rules stored on the plug |
| `GET /api/session` | Whether signing in is required, possible and done |
| `POST /api/session` | Sign in: send `{"password": "..."}` |
| `DELETE /api/session` | Sign out |

```
curl http://127.0.0.1:8080/api/status
curl -X POST -H 'Content-Type: application/json' -d '{"on": true}' http://127.0.0.1:8080/api/relay
curl -X PUT -H 'Content-Type: application/json' --data @schedule.json http://127.0.0.1:8080/api/schedule
curl http://127.0.0.1:8080/api/plug/rules
curl -X POST -H 'Content-Type: application/json' -d '{"days": ["mon", "fri"], "time": "07:30", "action": "on"}' http://127.0.0.1:8080/api/plug/rules
curl -X DELETE http://127.0.0.1:8080/api/plug/rules/00000000000000000000000000000001
```

Responses are JSON. An error has the form `{"error": "..."}` with a fitting
status code: 400 for a request that is wrong, 401 for one that has to sign
in or send the token first, 404 for a rule the plug does not have, 409 for a
rule of a kind that cannot be changed, 429 for a password tried too soon
after wrong ones, 502 when the plug could not be reached or did not do as
asked.
`GET /api/status` still answers 200 when the plug is unreachable, with
`"reachable": false`, because the rest of the answer does not depend on the
plug.

A `POST` or `PUT` must be sent as `application/json`.

`GET /api/plug/rules` answers with the plug's own schedule:

```json
{
  "enabled": true,
  "rules": [
    {
      "id": "00000000000000000000000000000001",
      "name": "morning",
      "enabled": true,
      "weekly": true,
      "repeats": true,
      "days": ["mon", "tue", "wed", "thu", "fri"],
      "at": "time",
      "time": "07:30",
      "action": "on"
    }
  ]
}
```

- `enabled` at the top says whether the plug's schedule as a whole is switched
  on. Each rule has its own `enabled` as well.
- `at` is `time`, `sunrise`, `sunset` or `unknown`. `time` is only present for
  a rule timed by the clock, and is by the plug's clock.
- `action` is `on`, `off` or `unknown`.
- `repeats` is false for a rule that runs once.
- `end` appears on a rule that does a second thing later, with its own `at`,
  `time` and `action`.
- `weekly` is true for the plain kind of rule, which switches the plug at a
  time of day on chosen days of every week and does nothing else. That is the
  kind the schedule kasad runs is made of, and the only kind that can be
  added or changed.

`POST /api/plug/rules` and `PUT /api/plug/rules/{id}` take a weekly rule in
the same terms, and answer with the rule as the plug then lists it:

```json
{
  "name": "morning",
  "enabled": true,
  "days": ["mon", "tue", "wed", "thu", "fri"],
  "time": "07:30",
  "action": "on"
}
```

`name` and `enabled` may be left out; a rule is enabled unless it says
otherwise. The plug chooses the id of a new rule.

### Who can use the web app and API

Without `-listen` there is neither. With it, anyone who can reach the address
can switch the plug and change its rules, so choose the address with care:
`127.0.0.1` keeps them to the machine itself.

Two environment variables of `serve` decide who else gets in:

| Variable | Who it is for | How it is shown |
|---|---|---|
| `KASAD_PASSWORD` | People, in the web app | Typed once on the sign-in screen |
| `KASAD_TOKEN` | Programs, such as scripts and `curl` | Sent with every request |

```
KASAD_PASSWORD='a phrase of your own' kasad -host 192.0.2.10 -data /var/lib/kasad -listen 0.0.0.0:8080 serve
```

With neither set, the web app and the API are open. With either set, every
API request has to show one of them. Set a password whenever kasad listens on
an address other machines can reach; `serve` logs a warning if it has neither.

**Signing in.** The web app asks for the password and then keeps the person
signed in on that browser for 30 days, or until they sign out.

- Signing in sets a cookie that scripts in the page cannot read and that the
  browser does not send along with requests started by other sites. The
  password itself is not kept in the browser.
- Restarting kasad does not sign anyone out. It keeps a record of who is
  signed in, in `sessions.json` in the data directory. That file is no use
  for signing in: for each session it holds a digest keyed with the password,
  and when the session runs out. It does not hold the password or the
  cookie's value, and neither can be worked out from it.
- To sign everyone out, stop kasad, delete `sessions.json` and start it
  again. Changing the password also does it, since a session only counts
  under the password it began with. Those sessions would count again if the
  old password were put back before they ran out, which deleting the file
  rules out.
- After five wrong passwords in a row, kasad makes the next attempt wait: a
  second at first, then twice as long each time, up to a minute. A right
  password clears the count. The count is shared by everyone, so someone on
  the network can keep others from signing in by guessing wrongly over and
  over. They cannot use it to get in, and people already signed in are not
  affected.
- A request that changes something is refused if a browser sent it on behalf
  of a page kasad did not serve.
- The web app's own files are served to anyone, since a browser has nothing
  to show when it first asks, and they hold nothing secret.

**Programs** send the token instead:

```
curl -H 'Authorization: Bearer the-token' http://192.0.2.20:8080/api/status
```

A kasad with a token but no password protects the API, but leaves nobody able
to sign in to the web app, which then says so.

**This is meant for a network you trust.** Everything is plain HTTP, so the
password, the token and the cookie can all be read by anyone who can watch
the traffic. To use kasad across a network you do not trust, put it behind
something that adds TLS. If that passes on `X-Forwarded-Proto: https`, the
cookie is marked for HTTPS only.

## Layout

- `protocol`: the wire protocol, which is JSON obfuscated with an autokey XOR
  cipher and sent with a 4-byte length prefix.
- `plug`: typed commands built on `protocol`, including reading and deleting
  the rules a plug stores.
- `schedule`: rules, the calculation of when they fall due, the loop that
  applies them, and the file-backed store they are kept in.
- `internal/api`: the JSON API, and signing in.
- `internal/web`: the web app. `static` holds the page as it is served, with
  no build step: plain HTML, CSS and JavaScript modules.
- `cmd/kasad`: the command-line tool.
- `internal/atomicfile`: replaces a file in one step, for the files kasad
  keeps in its data directory.
- `contrib`: a systemd unit, a Compose file and a settings file to start
  from.
- `Dockerfile`, `scripts/dist.sh`: how the image and the release archives
  are built.
- `.github/workflows`: the tests that run on every pull request, the making
  of a release, and the publishing of the project page.
- `internal/mockplug`: an in-memory mock plug, so the tests run without
  hardware.
- `site`: the project page. `static` holds the page as it is served, with no
  build step; `jstest` holds a test of its protocol demonstration.

## Security

The plug's protocol has no authentication. Anything that can reach port 9999
on a plug can control it, so keep plugs on a network that only trusted
machines can reach.

kasad's own web app and API can ask for a password and a token, but speak
plain HTTP. See "Who can use the web app and API" above for what that does
and does not protect.

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

Both sets run on every pull request.

The project page's demonstration of the protocol is checked against the same
test vector as the Go code. That test runs on any pull request that changes
the page:

```
cd site && node --test
```

## Releasing

A release is made from a version tag. Either push one:

```
git tag v1.2.3
git push origin v1.2.3
```

or publish a release on GitHub's Releases page and have it make a new tag of
that form. Either way the `Release` workflow then:

1. runs the tests;
2. builds the archives with `scripts/dist.sh` and publishes them as a GitHub
   release, with notes drawn from the pull requests merged since the last
   one. If the release was made on GitHub, the archives are added to it and
   its title and notes are left alone;
3. builds the image and pushes it to `ghcr.io` as `1.2.3` and, unless it is
   a pre-release, as `1.2` and `latest` too.

A tag with a hyphen, such as `v1.2.3-rc.1`, is published as a pre-release. A
release made on GitHub is a pre-release if it is marked as one there.

If a release's run fails, fix the cause on `main`, then run the workflow by
hand from the Actions page and give it the tag. It builds that tag again,
going by the workflow as it is on `main`.

A pull request that changes how a release is built goes through the same
steps without publishing anything. `scripts/dist.sh v0.0.0-try` builds the
archives into `dist/` by hand.

## Project page

`site/static` is the page at <https://kasad.gureet.ca/>: plain HTML, CSS
and JavaScript with no build step, and nothing loaded from anywhere else. It
follows the device's light or dark setting, and has a switch to choose one
instead.

The `Pages` workflow publishes it whenever a change to it reaches `main`. A
pull request that changes it runs its test and builds what would be
published, without publishing it. Publishing needs the repository's Pages
source set to GitHub Actions, once, under Settings, Pages. After that the
workflow can also be run by hand from the Actions page.

The address is the custom domain set in the same place, and GitHub's own
address for the page, `gureetk.github.io/kasad`, sends visitors there. The
page names its address in its `canonical` and `og:` tags, so change those
too if the domain ever changes.

To look at it before publishing, serve the folder, since its scripts are
modules and a browser will not run them from a file:

```
python3 -m http.server -d site/static
```

then open `http://localhost:8000/`.

The screenshots show the web app run against the mock plug the tests use,
with made-up rules. The page names the current release in two places, the
note at the top and the archive names under Install, so change both when
making a release.
