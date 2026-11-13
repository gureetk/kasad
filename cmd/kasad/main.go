// Command kasad controls a smart plug over the local network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/protocol"
)

const usage = `Usage: kasad [flags] <command>

Commands:
  state             show the plug's state and clock
  on                switch the relay on
  off               switch the relay off
  sync              set the plug's clock from this machine's clock
  rules             list the rules stored on the plug
  delete-rule <id>  delete one of the rules stored on the plug
  serve             run the schedule in the data directory, and keep the
                    plug's clock set
  version           show which version of kasad this is

The plug keeps local time with no UTC offset. state, sync and serve assume
that the plug is set to the same timezone as this machine.

The rules stored on the plug are ones it runs by itself, such as those made
with the vendor's app. They are separate from the schedule that serve runs.

serve can also offer a web app and a JSON API, at the address given with
-listen. Two environment variables decide who may use them:

  KASAD_PASSWORD  the password people sign in to the web app with
  KASAD_TOKEN     a token for programs to send with their API requests

With neither set, anyone who can reach the address can use both. Everything
is sent over plain HTTP, so this is meant for a network you trust.

Either can be kept in a file instead, named by KASAD_PASSWORD_FILE or
KASAD_TOKEN_FILE.

Every flag below can also be set through the environment, under the name
shown beside it. A flag on the command line wins.

Flags:
`

// envPrefix begins the name of every environment variable kasad reads.
const envPrefix = "KASAD_"

// hostEnv names the environment variable that supplies the plug's address
// when -host is not given.
const hostEnv = envPrefix + "HOST"

// envName returns the name of the environment variable that stands in for a
// flag: KASAD_SYNC_INTERVAL for -sync-interval, and so on.
func envName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// version is set when a release is built, with
// -ldflags "-X main.version=v1.2.3".
var version = ""

// versionString returns the version of this build. Without one built in, it
// is the version the Go tools recorded: the module's version for a build
// made with "go install", or one worked out from the repository.
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "unknown"
}

// commands lists the commands and how many arguments each one takes.
var commands = map[string]int{
	"state":       0,
	"on":          0,
	"off":         0,
	"sync":        0,
	"rules":       0,
	"delete-rule": 1,
	"serve":       0,
	"version":     0,
}

// Exit codes.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// environment is everything run takes from the outside world, so that tests
// can supply their own.
type environment struct {
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	now    func() time.Time

	// checkInterval overrides how often serve looks at the clock. Zero
	// means the scheduler's default.
	checkInterval time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], environment{
		stdout: os.Stdout,
		stderr: os.Stderr,
		getenv: os.Getenv,
		now:    time.Now,
	})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, env environment) int {
	flags := flag.NewFlagSet("kasad", flag.ContinueOnError)
	flags.SetOutput(env.stderr)
	host := flags.String("host", "", "plug address, as host or host:port")
	timeout := flags.Duration("timeout", protocol.DefaultTimeout, "how long to wait for the plug to answer")
	dataDir := flags.String("data", ".", "serve: directory holding "+scheduleFile+", and where progress is saved")
	syncInterval := flags.Duration("sync-interval", time.Hour, "serve: how often to set the plug's clock (0 to never set it)")
	listen := flags.String("listen", "", "serve: address to offer the web app and JSON API on, such as 127.0.0.1:8080 (default: neither)")
	flags.VisitAll(func(f *flag.Flag) {
		f.Usage += " [$" + envName(f.Name) + "]"
	})
	flags.Usage = func() {
		fmt.Fprint(env.stderr, usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if err := flagsFromEnv(flags, env); err != nil {
		fmt.Fprintf(env.stderr, "kasad: %v\n", err)
		return exitUsage
	}

	if flags.NArg() == 0 {
		fmt.Fprintln(env.stderr, "kasad: no command given")
		flags.Usage()
		return exitUsage
	}
	command := flags.Arg(0)
	wantArgs, known := commands[command]
	if !known {
		fmt.Fprintf(env.stderr, "kasad: unknown command %q\n", command)
		flags.Usage()
		return exitUsage
	}
	if flags.NArg()-1 != wantArgs {
		if wantArgs == 0 {
			fmt.Fprintln(env.stderr, "kasad: expected one command, got", flags.NArg())
		} else {
			fmt.Fprintf(env.stderr, "kasad: %s takes the id of one rule, as listed by the rules command\n", command)
		}
		flags.Usage()
		return exitUsage
	}

	if command == "version" {
		fmt.Fprintf(env.stdout, "kasad %s (%s, %s/%s)\n", versionString(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return exitOK
	}

	if *host == "" {
		fmt.Fprintf(env.stderr, "kasad: no plug address: pass -host or set %s\n", hostEnv)
		return exitUsage
	}
	if *timeout <= 0 {
		fmt.Fprintln(env.stderr, "kasad: -timeout must be positive")
		return exitUsage
	}
	if *syncInterval < 0 {
		fmt.Fprintln(env.stderr, "kasad: -sync-interval must not be negative")
		return exitUsage
	}

	p := plug.New(*host)
	p.Client.Timeout = *timeout

	var err error
	switch command {
	case "state":
		err = showState(ctx, p, env)
	case "on":
		err = switchRelay(ctx, p, true, env)
	case "off":
		err = switchRelay(ctx, p, false, env)
	case "sync":
		err = syncClock(ctx, p, env)
	case "rules":
		err = listRules(ctx, p, env)
	case "delete-rule":
		err = deleteRule(ctx, p, flags.Arg(1), env)
	case "serve":
		opts := serveOptions{
			dataDir:      *dataDir,
			syncInterval: *syncInterval,
			listen:       *listen,
		}
		if opts.token, err = secret(env, tokenEnv); err != nil {
			break
		}
		if opts.password, err = secret(env, passwordEnv); err != nil {
			break
		}
		err = serve(ctx, p, opts, env)
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "kasad: %v\n", err)
		return exitFailed
	}
	return exitOK
}

// flagsFromEnv gives each flag that was not on the command line the value of
// its environment variable, if that is set. The value is read as it would be
// on the command line.
func flagsFromEnv(flags *flag.FlagSet, env environment) error {
	given := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })

	var problem error
	flags.VisitAll(func(f *flag.Flag) {
		name := envName(f.Name)
		value := env.getenv(name)
		if given[f.Name] || value == "" || problem != nil {
			return
		}
		if err := f.Value.Set(value); err != nil {
			problem = fmt.Errorf("%s: %q is not a value -%s can take", name, value, f.Name)
		}
	})
	return problem
}

// secret returns the secret that the environment variable name stands for.
// That is the variable's own value or, if the variable name_FILE is set, the
// contents of the file it names, less the line ending a file usually has.
// Keeping a secret in a file keeps it out of the process's environment, and
// is how container runtimes and systemd hand secrets over.
func secret(env environment, name string) (string, error) {
	value, path := env.getenv(name), env.getenv(name+fileSuffix)
	switch {
	case path == "":
		return value, nil
	case value != "":
		return "", fmt.Errorf("%s and %s are both set; set only one", name, name+fileSuffix)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name+fileSuffix, err)
	}
	value = strings.TrimRight(string(data), "\r\n")
	if value == "" {
		// An empty secret would mean none at all, and so no protection.
		// Better to stop than to serve an open API by accident.
		return "", fmt.Errorf("%s: %s is empty", name+fileSuffix, path)
	}
	return value, nil
}

// showState prints a summary of the plug.
func showState(ctx context.Context, p *plug.Plug, env environment) error {
	status, err := p.Status(ctx)
	if err != nil {
		return err
	}
	info := status.Sysinfo
	now := env.now()

	state := "off"
	if info.On() {
		state = "on for " + info.OnFor().String()
	}
	// The plug reports wall-clock time with no UTC offset, so compare it
	// with this machine's local time.
	clock := status.Clock.In(now.Location())

	w := env.stdout
	fmt.Fprintf(w, "%s (%s)\n", info.Alias, info.Model)
	fmt.Fprintf(w, "  State:     %s\n", state)
	fmt.Fprintf(w, "  Clock:     %s, %s\n", clock.Format("2006-01-02 15:04:05"), describeOffset(clock.Sub(now)))
	fmt.Fprintf(w, "  Timezone:  index %d\n", status.Clock.ZoneIndex)
	fmt.Fprintf(w, "  Hardware:  %s\n", info.HardwareVersion)
	fmt.Fprintf(w, "  Firmware:  %s\n", info.SoftwareVersion)
	fmt.Fprintf(w, "  MAC:       %s\n", info.MAC)
	fmt.Fprintf(w, "  Signal:    %d dBm\n", info.RSSI)
	fmt.Fprintf(w, "  LED:       %s\n", onOff(info.LEDOn()))
	return nil
}

// clockTolerance is how far the plug's clock may be from this machine's
// before the difference is reported. The plug reports whole seconds and the
// exchange itself takes time, so a second or so of difference means nothing.
const clockTolerance = 2 * time.Second

// describeOffset puts into words how far the plug's clock is from this
// machine's. A positive offset means the plug is ahead.
func describeOffset(offset time.Duration) string {
	offset = offset.Round(time.Second)
	switch {
	case offset > -clockTolerance && offset < clockTolerance:
		return "in step with this machine"
	case offset > 0:
		return formatOffset(offset) + " ahead of this machine"
	default:
		return formatOffset(-offset) + " behind this machine"
	}
}

// formatOffset renders a positive offset, switching to whole days once hours
// stop being a useful unit. A plug that has lost its clock can be years out.
func formatOffset(offset time.Duration) string {
	const day = 24 * time.Hour
	if offset >= 2*day {
		return fmt.Sprintf("%d days", offset/day)
	}
	return offset.String()
}

// switchRelay turns the relay on or off and reports the result.
func switchRelay(ctx context.Context, p *plug.Plug, on bool, env environment) error {
	info, err := p.Switch(ctx, on)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "%s is %s\n", info.Alias, onOff(on))
	return nil
}

// syncClock sets the plug's clock to this machine's local time, then reads it
// back to confirm that the plug took it.
func syncClock(ctx context.Context, p *plug.Plug, env environment) error {
	before, err := p.Status(ctx)
	if err != nil {
		return err
	}
	now := env.now()
	was := describeOffset(before.Clock.In(now.Location()).Sub(now))

	// The plug stores whole seconds, so send the nearest one.
	target := env.now().Round(time.Second)
	if err := p.SetClock(ctx, target); err != nil {
		return err
	}

	after, err := p.Clock(ctx)
	if err != nil {
		return fmt.Errorf("set the clock, but could not confirm it: %w", err)
	}
	now = env.now()
	offset := after.In(now.Location()).Sub(now).Round(time.Second)
	if offset <= -clockTolerance || offset >= clockTolerance {
		return fmt.Errorf("%s accepted the new time but its clock is still %s",
			before.Sysinfo.Alias, describeOffset(offset))
	}
	fmt.Fprintf(env.stdout, "%s clock set to %s (was %s)\n",
		before.Sysinfo.Alias, target.Format("2006-01-02 15:04:05"), was)
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
