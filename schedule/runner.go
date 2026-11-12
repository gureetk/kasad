package schedule

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"
)

// Defaults for a Runner's timing.
const (
	// DefaultCheckInterval is the longest a Runner sleeps before it looks
	// at the clock again. Waking regularly, rather than sleeping until the
	// next event, keeps the schedule right when the machine's clock is
	// changed or the machine is suspended.
	DefaultCheckInterval = time.Minute

	// DefaultRetryInterval is how long a Runner waits before trying again
	// after an event could not be applied.
	DefaultRetryInterval = 30 * time.Second
)

// Runner applies a schedule's events as they fall due.
//
// When it starts, it catches up: if events fell due while it was not
// running, it applies the most recent one. Earlier ones are skipped, since
// the latest decides what state the plug should be in. If the schedule is
// new or has changed since the last run, there is nothing to catch up on and
// the Runner starts from the present.
//
// An event that cannot be applied is retried until it succeeds or a later
// event takes its place.
type Runner struct {
	// Schedule is the schedule to run.
	Schedule Schedule

	// Apply carries out an event. It is retried if it returns an error.
	Apply func(ctx context.Context, event Event) error

	// State remembers progress between runs. It may be nil, in which case
	// nothing is remembered and every run starts from the present.
	State StateStore

	// Now returns the current time. It defaults to time.Now. Rule times
	// are read in the location of the times it returns.
	Now func() time.Time

	// Logger receives a record of what the Runner does. It defaults to
	// discarding everything.
	Logger *slog.Logger

	// CheckInterval and RetryInterval override the defaults above.
	CheckInterval time.Duration
	RetryInterval time.Duration

	// sleep waits for d or until ctx ends. Tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// Run applies events until ctx ends, then returns nil. It returns an error
// only if the Runner has no Apply function or the saved state cannot be
// read.
func (r *Runner) Run(ctx context.Context) error {
	if r.Apply == nil {
		return ErrNoApply
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	logger := r.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	checkInterval := r.CheckInterval
	if checkInterval <= 0 {
		checkInterval = DefaultCheckInterval
	}
	retryInterval := r.RetryInterval
	if retryInterval <= 0 {
		retryInterval = DefaultRetryInterval
	}
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepContext
	}

	save := func(state State) {
		if r.State == nil {
			return
		}
		if err := r.State.Save(state); err != nil {
			logger.Error("could not save schedule state", "error", err)
		}
	}

	var state State
	if r.State != nil {
		loaded, err := r.State.Load()
		if err != nil {
			return err
		}
		state = loaded
	}
	digest := r.Schedule.Digest()
	if state.Since.IsZero() || state.Digest != digest {
		// A new or changed schedule takes effect from now on. Events it
		// would have had in the past are not applied.
		state = State{Since: now(), Digest: digest}
		save(state)
		logger.Info("schedule is new or has changed, starting from now")
	}

	failing := false
	for {
		current := now()
		if state.Since.After(current) {
			// The clock has gone backwards past the saved point. Without
			// this, nothing would fire until the clock caught up again.
			logger.Warn("clock is behind the saved schedule state, starting from now")
			state.Since = current
			save(state)
		}

		wait := checkInterval
		if event, ok := r.Schedule.Due(state.Since, current); ok {
			err := r.Apply(ctx, event)
			switch {
			case err == nil:
				state.Since = event.At
				save(state)
				logger.Info("applied rule",
					"rule", event.Rule.ID, "action", string(event.Rule.Action),
					"scheduled", event.At.Format(time.RFC3339), "late", current.Sub(event.At).Round(time.Second).String())
				failing = false
			case ctx.Err() != nil:
				return nil
			default:
				// Report a run of failures once, not on every retry.
				if !failing {
					logger.Error("could not apply rule, will keep trying",
						"rule", event.Rule.ID, "action", string(event.Rule.Action), "error", err)
				}
				failing = true
				wait = retryInterval
			}
		}

		if next, ok := r.Schedule.Next(current); ok {
			if until := next.At.Sub(current); until < wait {
				wait = until
			}
		}
		if err := sleep(ctx, wait); err != nil {
			return nil
		}
	}
}

// sleepContext waits for d, or until ctx ends, in which case it returns
// ctx's error.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrNoApply is returned by Run when the Runner has no Apply function.
var ErrNoApply = errors.New("schedule: runner has no Apply function")
