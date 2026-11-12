// Package mockplug is an in-memory stand-in for a smart plug, for tests that
// should run without hardware.
//
// It listens on a loopback port and speaks the same wire protocol as a real
// device, but understands only a small set of commands: system.get_sysinfo,
// system.set_relay_state, time.get_time, time.get_timezone and time.set_time.
//
// The field names in its replies, and the reply to an unknown method, follow
// what real hardware sends. Every value is a placeholder, and the reply to an
// unknown module is an approximation.
package mockplug

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/gureetk/kasad/protocol"
)

// Error codes used in replies.
const (
	errModuleNotSupported = -1
	errMethodNotSupported = -2
	errInvalidArgument    = -3
)

// Plug is a running mock device. Create one with New and stop it with Close.
type Plug struct {
	listener net.Listener
	wg       sync.WaitGroup

	mu          sync.Mutex
	relayOn     bool
	onSince     time.Time
	now         func() time.Time
	clockOffset time.Duration
	zoneIndex   int
	requests    int
	closed      bool
	conns       map[net.Conn]struct{}
}

// New starts a mock plug on a free loopback port. Its relay starts off, and
// its clock follows the system clock until SetClock is called.
func New() (*Plug, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Plug{
		listener: listener,
		now:      time.Now,
		conns:    make(map[net.Conn]struct{}),
	}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Addr returns the host:port the plug is listening on.
func (p *Plug) Addr() string {
	return p.listener.Addr().String()
}

// RelayOn reports whether the relay is currently on.
func (p *Plug) RelayOn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.relayOn
}

// SetRelay switches the relay directly, as if someone had pressed the button
// on the device.
func (p *Plug) SetRelay(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setRelayLocked(on)
}

func (p *Plug) setRelayLocked(on bool) {
	if on && !p.relayOn {
		p.onSince = p.now()
	}
	p.relayOn = on
}

// SetClock replaces the plug's clock source and forgets any earlier set_time.
// The wall-clock fields of the time it returns are what the plug reports,
// whatever its location.
func (p *Plug) SetClock(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
	p.clockOffset = 0
}

// Time returns the plug's current reading of the time, including the effect
// of any set_time command.
func (p *Plug) Time() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deviceTimeLocked()
}

// SetTimezoneIndex sets the timezone index the plug reports.
func (p *Plug) SetTimezoneIndex(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.zoneIndex = index
}

// Requests returns how many requests the plug has answered.
func (p *Plug) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// Close stops the plug, drops any open connections, and waits for its
// goroutines to finish. It is safe to call more than once.
func (p *Plug) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	err := p.listener.Close()
	for conn := range p.conns {
		conn.Close()
	}
	p.mu.Unlock()

	p.wg.Wait()
	return err
}

func (p *Plug) serve() {
	defer p.wg.Done()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if !p.track(conn) {
			conn.Close()
			return
		}
		p.wg.Add(1)
		go p.handle(conn)
	}
}

// track records conn so that Close can drop it. It reports false if the plug
// has already been closed.
func (p *Plug) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *Plug) untrack(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, conn)
}

// handle answers requests on conn until the peer hangs up or sends something
// that cannot be answered.
func (p *Plug) handle(conn net.Conn) {
	defer p.wg.Done()
	defer p.untrack(conn)
	defer conn.Close()

	for {
		request, err := protocol.ReadMessage(conn)
		if err != nil {
			return
		}
		reply, ok := p.respond(request)
		if !ok {
			return
		}
		if err := protocol.WriteMessage(conn, reply); err != nil {
			return
		}
	}
}

// respond builds the reply to one request. It reports false if the request
// is not a JSON object of modules, in which case the connection is dropped
// without a reply.
func (p *Plug) respond(request []byte) ([]byte, bool) {
	var modules map[string]map[string]json.RawMessage
	if err := json.Unmarshal(request, &modules); err != nil {
		return nil, false
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++

	reply := make(map[string]any, len(modules))
	for module, methods := range modules {
		var run func(method string, args json.RawMessage) any
		switch module {
		case "system":
			run = p.systemLocked
		case "time":
			run = p.timeLocked
		default:
			reply[module] = status{Code: errModuleNotSupported, Message: "module not support"}
			continue
		}
		results := make(map[string]any, len(methods))
		for method, args := range methods {
			results[method] = run(method, args)
		}
		reply[module] = results
	}

	encoded, err := json.Marshal(reply)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// status is the outcome of a command. A zero code means success.
type status struct {
	Code    int    `json:"err_code"`
	Message string `json:"err_msg,omitempty"`
}

var methodNotSupported = status{Code: errMethodNotSupported, Message: "member not support"}

// systemLocked runs one method of the "system" module. The caller holds
// p.mu.
func (p *Plug) systemLocked(method string, args json.RawMessage) any {
	switch method {
	case "get_sysinfo":
		return p.sysinfoLocked()
	case "set_relay_state":
		var params struct {
			State *int `json:"state"`
		}
		if err := json.Unmarshal(args, &params); err != nil || params.State == nil ||
			(*params.State != 0 && *params.State != 1) {
			return status{Code: errInvalidArgument, Message: "invalid argument"}
		}
		p.setRelayLocked(*params.State == 1)
		return status{}
	default:
		return methodNotSupported
	}
}

// timeLocked runs one method of the "time" module. The caller holds p.mu.
func (p *Plug) timeLocked(method string, args json.RawMessage) any {
	switch method {
	case "get_time":
		now := p.deviceTimeLocked()
		return timeReply{
			Year:   now.Year(),
			Month:  int(now.Month()),
			Day:    now.Day(),
			Hour:   now.Hour(),
			Minute: now.Minute(),
			Second: now.Second(),
		}
	case "get_timezone":
		return timezoneReply{Index: p.zoneIndex}
	case "set_time":
		var params struct {
			Year   *int `json:"year"`
			Month  *int `json:"month"`
			Day    *int `json:"mday"`
			Hour   *int `json:"hour"`
			Minute *int `json:"min"`
			Second *int `json:"sec"`
		}
		invalid := status{Code: errInvalidArgument, Message: "invalid argument"}
		if err := json.Unmarshal(args, &params); err != nil {
			return invalid
		}
		for _, field := range []*int{params.Year, params.Month, params.Day, params.Hour, params.Minute, params.Second} {
			if field == nil {
				return invalid
			}
		}
		now := p.now()
		set := time.Date(*params.Year, time.Month(*params.Month), *params.Day,
			*params.Hour, *params.Minute, *params.Second, 0, now.Location())
		// time.Date normalises out-of-range fields, such as month 13, into
		// a different date. A value that does not survive unchanged was
		// not a real date and time.
		if set.Year() != *params.Year || int(set.Month()) != *params.Month || set.Day() != *params.Day ||
			set.Hour() != *params.Hour || set.Minute() != *params.Minute || set.Second() != *params.Second {
			return invalid
		}
		p.clockOffset = set.Sub(now)
		return status{}
	default:
		return methodNotSupported
	}
}

// deviceTimeLocked returns the plug's own reading of the time: its clock
// source, shifted by whatever set_time last asked for. The caller holds p.mu.
func (p *Plug) deviceTimeLocked() time.Time {
	return p.now().Add(p.clockOffset)
}

type timeReply struct {
	Year      int `json:"year"`
	Month     int `json:"month"`
	Day       int `json:"mday"`
	Hour      int `json:"hour"`
	Minute    int `json:"min"`
	Second    int `json:"sec"`
	ErrorCode int `json:"err_code"`
}

type timezoneReply struct {
	Index     int `json:"index"`
	ErrorCode int `json:"err_code"`
}

// sysinfo is the reply to get_sysinfo. Every identifying value is a
// placeholder.
type sysinfo struct {
	SoftwareVersion string     `json:"sw_ver"`
	HardwareVersion string     `json:"hw_ver"`
	Model           string     `json:"model"`
	DeviceID        string     `json:"deviceId"`
	OEMID           string     `json:"oemId"`
	HardwareID      string     `json:"hwId"`
	RSSI            int        `json:"rssi"`
	Latitude        int        `json:"latitude_i"`
	Longitude       int        `json:"longitude_i"`
	Alias           string     `json:"alias"`
	Status          string     `json:"status"`
	MICType         string     `json:"mic_type"`
	Feature         string     `json:"feature"`
	MAC             string     `json:"mac"`
	Updating        int        `json:"updating"`
	LEDOff          int        `json:"led_off"`
	RelayState      int        `json:"relay_state"`
	OnTime          int        `json:"on_time"`
	IconHash        string     `json:"icon_hash"`
	DeviceName      string     `json:"dev_name"`
	ActiveMode      string     `json:"active_mode"`
	NextAction      nextAction `json:"next_action"`
	ErrorCode       int        `json:"err_code"`
}

// nextAction describes the next scheduled action. The mock has no schedule,
// so it always reports a type of -1.
type nextAction struct {
	Type int `json:"type"`
}

// sysinfoLocked builds the get_sysinfo reply. The caller holds p.mu.
func (p *Plug) sysinfoLocked() sysinfo {
	relayState, onTime := 0, 0
	if p.relayOn {
		relayState = 1
		onTime = int(p.now().Sub(p.onSince) / time.Second)
	}
	return sysinfo{
		SoftwareVersion: "0.0.0 Build 000000 Rel.000000",
		HardwareVersion: "1.0",
		Model:           "HS103(US)",
		DeviceID:        "0000000000000000000000000000000000000000",
		OEMID:           "00000000000000000000000000000000",
		HardwareID:      "00000000000000000000000000000000",
		RSSI:            -50,
		Alias:           "Mock Plug",
		Status:          "new",
		MICType:         "IOT.SMARTPLUGSWITCH",
		Feature:         "TIM",
		MAC:             "02:00:00:00:00:01",
		RelayState:      relayState,
		OnTime:          onTime,
		DeviceName:      "Smart Wi-Fi Plug Lite",
		ActiveMode:      "none",
		NextAction:      nextAction{Type: -1},
	}
}
