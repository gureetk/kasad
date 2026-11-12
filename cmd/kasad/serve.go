package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/schedule"
)

// The files serve keeps in its data directory.
const (
	scheduleFile = "schedule.json"
	stateFile    = "state.json"
)

// clockRetryInterval is how long serve waits before trying again after it
// could not set the plug's clock.
const clockRetryInterval = time.Minute

type serveOptions struct {
	dataDir      string
	syncInterval time.Duration // zero means never set the plug's clock
}

// serve runs the schedule in the data directory against the plug, and keeps
// the plug's clock set, until ctx ends.
func serve(ctx context.Context, p *plug.Plug, opts serveOptions, env environment) error {
	logger := slog.New(slog.NewTextHandler(env.stderr, nil))

	if info, err := os.Stat(opts.dataDir); err != nil {
		return fmt.Errorf("data directory: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("data directory %s is not a directory", opts.dataDir)
	}

	schedulePath := filepath.Join(opts.dataDir, scheduleFile)
	rules, err := schedule.Load(schedulePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		logger.Warn("no schedule file, running with no rules", "path", schedulePath)
	case err != nil:
		return err
	}

	enabled := 0
	for _, rule := range rules.Rules {
		if !rule.Disabled {
			enabled++
		}
	}
	logger.Info("starting", "plug", p.Addr, "rules", len(rules.Rules), "enabled", enabled)
	if next, ok := rules.Next(env.now()); ok {
		logger.Info("next event",
			"rule", next.Rule.ID, "action", string(next.Rule.Action), "at", next.At.Format(time.RFC3339))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var background sync.WaitGroup
	if opts.syncInterval > 0 {
		background.Add(1)
		go func() {
			defer background.Done()
			keepClockSet(ctx, p, opts.syncInterval, env, logger)
		}()
	}

	runner := &schedule.Runner{
		Schedule:      rules,
		State:         schedule.StateFile{Path: filepath.Join(opts.dataDir, stateFile)},
		Now:           env.now,
		Logger:        logger,
		CheckInterval: env.checkInterval,
		Apply: func(ctx context.Context, event schedule.Event) error {
			_, err := switchAndConfirm(ctx, p, event.Rule.Action == schedule.On)
			return err
		},
	}
	err = runner.Run(ctx)

	cancel()
	background.Wait()
	logger.Info("stopped")
	return err
}

// keepClockSet sets the plug's clock from this machine's, now and then every
// interval, until ctx ends. A plug that is kept off the internet has no other
// way to learn the time.
func keepClockSet(ctx context.Context, p *plug.Plug, interval time.Duration, env environment, logger *slog.Logger) {
	failing := false
	for {
		wait := interval
		offset, err := setClock(ctx, p, env)
		switch {
		case err == nil:
			if offset <= -clockTolerance || offset >= clockTolerance {
				logger.Info("corrected the plug's clock", "was", describeOffset(offset))
			} else if failing {
				logger.Info("the plug's clock can be set again")
			}
			failing = false
		case ctx.Err() != nil:
			return
		default:
			// Report a run of failures once, not on every retry.
			if !failing {
				logger.Error("could not set the plug's clock, will keep trying", "error", err)
			}
			failing = true
			if clockRetryInterval < wait {
				wait = clockRetryInterval
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// setClock sets the plug's clock to this machine's local time and returns how
// far out the plug's clock was beforehand. A positive offset means the plug
// was ahead.
func setClock(ctx context.Context, p *plug.Plug, env environment) (time.Duration, error) {
	before, err := p.Clock(ctx)
	if err != nil {
		return 0, err
	}
	now := env.now()
	offset := before.In(now.Location()).Sub(now).Round(time.Second)

	// The plug stores whole seconds, so send the nearest one.
	if err := p.SetClock(ctx, env.now().Round(time.Second)); err != nil {
		return 0, err
	}
	return offset, nil
}
