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
	"time"

	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/protocol"
)

const usage = `Usage: kasad [flags] <command>

Commands:
  state   show the plug's state and clock
  on      switch the relay on
  off     switch the relay off
  sync    set the plug's clock from this machine's clock

The plug keeps local time with no UTC offset. state and sync assume that the
plug is set to the same timezone as this machine.

Flags:
`

// hostEnv names the environment variable that supplies the plug's address
// when -host is not given.
const hostEnv = "KASAD_HOST"

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
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
	host := flags.String("host", "", "plug address, as host or host:port (default: $"+hostEnv+")")
	timeout := flags.Duration("timeout", protocol.DefaultTimeout, "how long to wait for the plug to answer")
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

	if flags.NArg() != 1 {
		if flags.NArg() == 0 {
			fmt.Fprintln(env.stderr, "kasad: no command given")
		} else {
			fmt.Fprintln(env.stderr, "kasad: expected one command, got", flags.NArg())
		}
		flags.Usage()
		return exitUsage
	}
	command := flags.Arg(0)
	switch command {
	case "state", "on", "off", "sync":
	default:
		fmt.Fprintf(env.stderr, "kasad: unknown command %q\n", command)
		flags.Usage()
		return exitUsage
	}

	if *host == "" {
		*host = env.getenv(hostEnv)
	}
	if *host == "" {
		fmt.Fprintf(env.stderr, "kasad: no plug address: pass -host or set %s\n", hostEnv)
		return exitUsage
	}
	if *timeout <= 0 {
		fmt.Fprintln(env.stderr, "kasad: -timeout must be positive")
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
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "kasad: %v\n", err)
		return exitFailed
	}
	return exitOK
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

// switchRelay turns the relay on or off, then reads the state back to confirm
// that the plug did it.
func switchRelay(ctx context.Context, p *plug.Plug, on bool, env environment) error {
	if err := p.SetRelay(ctx, on); err != nil {
		return err
	}
	info, err := p.Sysinfo(ctx)
	if err != nil {
		return fmt.Errorf("sent the command, but could not confirm the new state: %w", err)
	}
	if info.On() != on {
		return fmt.Errorf("%s accepted the command but is still %s", info.Alias, onOff(info.On()))
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
