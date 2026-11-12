package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gureetk/kasad/internal/api"
	"github.com/gureetk/kasad/internal/web"
	"github.com/gureetk/kasad/plug"
	"github.com/gureetk/kasad/schedule"
)

// The files serve keeps in its data directory.
const (
	scheduleFile = "schedule.json"
	stateFile    = "state.json"
)

// tokenEnv names the environment variable that holds the API token. It is
// read from the environment, not from a flag, so that it does not show up in
// the process list.
const tokenEnv = "KASAD_TOKEN"

// clockRetryInterval is how long serve waits before trying again after it
// could not set the plug's clock.
const clockRetryInterval = time.Minute

// shutdownTimeout is how long serve gives requests in progress to finish
// when it stops.
const shutdownTimeout = 5 * time.Second

type serveOptions struct {
	dataDir      string
	syncInterval time.Duration // zero means never set the plug's clock
	listen       string        // empty means no web app or API
	token        string        // empty means the API asks for no token
}

// serve runs the schedule in the data directory against the plug, keeps the
// plug's clock set and, if asked to, serves the web app and the API, until ctx
// ends.
func serve(ctx context.Context, p *plug.Plug, opts serveOptions, env environment) error {
	handler := slog.NewTextHandler(env.stderr, nil)
	logger := slog.New(handler)

	if info, err := os.Stat(opts.dataDir); err != nil {
		return fmt.Errorf("data directory: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("data directory %s is not a directory", opts.dataDir)
	}

	schedulePath := filepath.Join(opts.dataDir, scheduleFile)
	if _, err := os.Stat(schedulePath); errors.Is(err, fs.ErrNotExist) {
		logger.Warn("no schedule file, starting with no rules", "path", schedulePath)
	}
	store, err := schedule.OpenStore(schedulePath)
	if err != nil {
		return err
	}

	// Claim the address to listen on before anything else starts, so that a
	// bad address or a port already in use stops serve at once.
	var listener net.Listener
	if opts.listen != "" {
		listener, err = net.Listen("tcp", opts.listen)
		if err != nil {
			return fmt.Errorf("-listen: %w", err)
		}
	}

	rules := store.Schedule()
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

	if listener != nil {
		// The web app's files are open to anyone who can reach the address.
		// Everything they can do goes through the API, which is where the
		// token is checked.
		mux := http.NewServeMux()
		mux.Handle("/api/", (&api.Server{
			Plug:   p,
			Store:  store,
			Token:  opts.token,
			Now:    env.now,
			Logger: logger,
		}).Handler())
		mux.Handle("/", web.Handler())

		server := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			ErrorLog:          slog.NewLogLogger(handler, slog.LevelWarn),
		}
		logger.Info("serving the web app and API", "addr", listener.Addr().String(), "token", opts.token != "")
		if opts.token == "" && !isLoopback(listener.Addr()) {
			logger.Warn("the web app and API can be reached from other machines and ask for no token; set " + tokenEnv + " to require one")
		}

		background.Add(2)
		go func() {
			defer background.Done()
			if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
				logger.Error("the web app and API stopped unexpectedly", "error", err)
				cancel()
			}
		}()
		go func() {
			defer background.Done()
			<-ctx.Done()
			shutdownCtx, stop := context.WithTimeout(context.Background(), shutdownTimeout)
			defer stop()
			if err := server.Shutdown(shutdownCtx); err != nil {
				server.Close()
			}
		}()
	}

	if opts.syncInterval > 0 {
		background.Add(1)
		go func() {
			defer background.Done()
			keepClockSet(ctx, p, opts.syncInterval, env, logger)
		}()
	}

	runner := &schedule.Runner{
		Schedule:      store.Schedule,
		Wake:          store.Changes(),
		State:         schedule.StateFile{Path: filepath.Join(opts.dataDir, stateFile)},
		Now:           env.now,
		Logger:        logger,
		CheckInterval: env.checkInterval,
		Apply: func(ctx context.Context, event schedule.Event) error {
			_, err := p.Switch(ctx, event.Rule.Action == schedule.On)
			return err
		},
	}
	err = runner.Run(ctx)

	cancel()
	background.Wait()
	logger.Info("stopped")
	return err
}

// isLoopback reports whether addr can only be reached from this machine.
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
