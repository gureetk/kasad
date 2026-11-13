package plug

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotSwitched is returned by Switch when the device accepts the command
// but its relay does not change.
var ErrNotSwitched = errors.New("device accepted the command but did not switch")

// Sysinfo is the device's description of itself and its current state.
type Sysinfo struct {
	Alias           string `json:"alias"`
	DeviceName      string `json:"dev_name"`
	Model           string `json:"model"`
	HardwareVersion string `json:"hw_ver"`
	SoftwareVersion string `json:"sw_ver"`
	MAC             string `json:"mac"`
	DeviceID        string `json:"deviceId"`

	// RelayState is 1 when the relay is on and 0 when it is off.
	RelayState int `json:"relay_state"`

	// OnTime is how long the relay has been on, in seconds. It is zero
	// while the relay is off.
	OnTime int `json:"on_time"`

	// RSSI is the Wi-Fi signal strength in dBm.
	RSSI int `json:"rssi"`

	// LEDOff is 1 when the status LED is disabled.
	LEDOff int `json:"led_off"`
}

// On reports whether the relay is on.
func (s Sysinfo) On() bool {
	return s.RelayState == 1
}

// OnFor returns how long the relay has been on.
func (s Sysinfo) OnFor() time.Duration {
	return time.Duration(s.OnTime) * time.Second
}

// LEDOn reports whether the status LED is enabled.
func (s Sysinfo) LEDOn() bool {
	return s.LEDOff == 0
}

// Clock is the device's own reading of the date and time.
//
// The device reports wall-clock fields with no UTC offset. Its timezone
// setting is reported separately, as an index into the vendor's table of
// timezones.
type Clock struct {
	Year   int
	Month  int
	Day    int
	Hour   int
	Minute int
	Second int

	// ZoneIndex is the device's timezone setting.
	ZoneIndex int
}

// In returns the reading as a time in loc, which should be the timezone the
// device is set to.
func (c Clock) In(loc *time.Location) time.Time {
	return time.Date(c.Year, time.Month(c.Month), c.Day, c.Hour, c.Minute, c.Second, 0, loc)
}

// Status is a device's Sysinfo and Clock read in a single exchange.
type Status struct {
	Sysinfo Sysinfo
	Clock   Clock
}

// wireTime is the result of time.get_time and the argument of time.set_time.
// wireTimezone is the result of time.get_timezone.
type wireTime struct {
	Year   int `json:"year"`
	Month  int `json:"month"`
	Day    int `json:"mday"`
	Hour   int `json:"hour"`
	Minute int `json:"min"`
	Second int `json:"sec"`
}

type wireTimezone struct {
	Index int `json:"index"`
}

func newClock(t wireTime, zone wireTimezone) Clock {
	return Clock{
		Year:      t.Year,
		Month:     t.Month,
		Day:       t.Day,
		Hour:      t.Hour,
		Minute:    t.Minute,
		Second:    t.Second,
		ZoneIndex: zone.Index,
	}
}

// Sysinfo reads the device's description and current state.
func (p *Plug) Sysinfo(ctx context.Context) (Sysinfo, error) {
	var info Sysinfo
	err := p.call(ctx, command{module: "system", method: "get_sysinfo", result: &info})
	if err != nil {
		return Sysinfo{}, err
	}
	return info, nil
}

// SetRelay switches the relay on or off.
func (p *Plug) SetRelay(ctx context.Context, on bool) error {
	state := 0
	if on {
		state = 1
	}
	return p.call(ctx, command{
		module: "system",
		method: "set_relay_state",
		params: map[string]int{"state": state},
	})
}

// Switch turns the relay on or off, then reads the state back to confirm that
// the device did it. It returns the state it read.
func (p *Plug) Switch(ctx context.Context, on bool) (Sysinfo, error) {
	if err := p.SetRelay(ctx, on); err != nil {
		return Sysinfo{}, err
	}
	info, err := p.Sysinfo(ctx)
	if err != nil {
		return Sysinfo{}, fmt.Errorf("plug: sent the command, but could not confirm the new state: %w", err)
	}
	if info.On() != on {
		return info, fmt.Errorf("plug: %w: asked for %s, still %s", ErrNotSwitched, onOff(on), onOff(info.On()))
	}
	return info, nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// Clock reads the device's date, time and timezone setting.
func (p *Plug) Clock(ctx context.Context) (Clock, error) {
	var (
		t    wireTime
		zone wireTimezone
	)
	err := p.call(ctx,
		command{module: "time", method: "get_time", result: &t},
		command{module: "time", method: "get_timezone", result: &zone},
	)
	if err != nil {
		return Clock{}, err
	}
	return newClock(t, zone), nil
}

// SetClock sets the device's date and time to the wall-clock reading of t,
// to the second.
//
// Only the reading is sent. The device's timezone setting is left as it is,
// so t should be in the timezone the device is set to.
func (p *Plug) SetClock(ctx context.Context, t time.Time) error {
	return p.call(ctx, command{
		module: "time",
		method: "set_time",
		params: wireTime{
			Year:   t.Year(),
			Month:  int(t.Month()),
			Day:    t.Day(),
			Hour:   t.Hour(),
			Minute: t.Minute(),
			Second: t.Second(),
		},
	})
}

// Status reads the device's Sysinfo and Clock together, in one exchange.
func (p *Plug) Status(ctx context.Context) (Status, error) {
	var (
		info Sysinfo
		t    wireTime
		zone wireTimezone
	)
	err := p.call(ctx,
		command{module: "system", method: "get_sysinfo", result: &info},
		command{module: "time", method: "get_time", result: &t},
		command{module: "time", method: "get_timezone", result: &zone},
	)
	if err != nil {
		return Status{}, err
	}
	return Status{Sysinfo: info, Clock: newClock(t, zone)}, nil
}
