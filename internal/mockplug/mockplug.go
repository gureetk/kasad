// Package mockplug is an in-memory stand-in for a smart plug, for tests that
// should run without hardware.
//
// It listens on a loopback port and speaks the same wire protocol as a real
// device, but understands only a small set of commands: system.get_sysinfo,
// system.set_relay_state, time.get_time, time.get_timezone, time.set_time,
// schedule.get_rules, schedule.add_rule, schedule.edit_rule and
// schedule.delete_rule.
//
// The field names in its replies, and the reply to an unknown method, follow
// what real hardware sends. Every value is a placeholder. The replies to an
// unknown module, to adding or changing a rule, and to naming a rule that
// does not exist are approximations.
//
// The mock stores the rules of its schedule but does not run them: its relay
// only moves when it is told to.
package mockplug

import (
	"encoding/json"
	"errors"
	"fmt"
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
	stuck       bool
	onSince     time.Time
	now         func() time.Time
	clockOffset time.Duration
	zoneIndex   int
	rules       []Rule
	scheduleOff bool
	forgetful   bool
	ruleIDs     int
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

// SetStuck makes the plug accept relay commands without acting on them, as
// a device with a failed relay might.
func (p *Plug) SetStuck(stuck bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stuck = stuck
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

// Rule is a rule in the plug's own schedule, in the terms the device uses on
// the wire.
type Rule struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Enable is 1 when the rule is switched on and 0 when it is not.
	Enable int `json:"enable"`

	// Repeat is 1 for a rule that runs every week.
	Repeat int `json:"repeat"`

	// Weekdays has a 1 for each day the rule runs on, Sunday first.
	Weekdays [7]int `json:"wday"`

	// StartOption is 0 for a time of day, 1 for sunrise and 2 for sunset.
	// StartMinute is the time of day in minutes after midnight.
	StartOption int `json:"stime_opt"`
	StartMinute int `json:"smin"`

	// StartAction is 1 to switch on and 0 to switch off. EndAction is -1
	// for a rule with no second action.
	StartAction int `json:"sact"`
	EndAction   int `json:"eact"`
}

// SetRules replaces the rules in the plug's schedule.
func (p *Plug) SetRules(rules ...Rule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append([]Rule(nil), rules...)
}

// Rules returns the rules in the plug's schedule.
func (p *Plug) Rules() []Rule {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Rule(nil), p.rules...)
}

// SetScheduleEnabled sets whether the plug reports its schedule as a whole
// as switched on. It starts switched on.
func (p *Plug) SetScheduleEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduleOff = !enabled
}

// SetForgetful makes the plug accept new and changed rules without storing
// them, as a device that misunderstood the request might.
func (p *Plug) SetForgetful(forgetful bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetful = forgetful
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
		case "schedule":
			run = p.scheduleLocked
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

var (
	methodNotSupported = status{Code: errMethodNotSupported, Message: "member not support"}
	invalidArgument    = status{Code: errInvalidArgument, Message: "invalid argument"}
)

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
			return invalidArgument
		}
		if !p.stuck {
			p.setRelayLocked(*params.State == 1)
		}
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
		invalid := invalidArgument
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

// scheduleLocked runs one method of the "schedule" module. The caller holds
// p.mu.
func (p *Plug) scheduleLocked(method string, args json.RawMessage) any {
	switch method {
	case "get_rules":
		enable := 1
		if p.scheduleOff {
			enable = 0
		}
		return rulesReply{
			Rules:   append([]Rule{}, p.rules...),
			Version: 2,
			Enable:  enable,
		}
	case "add_rule":
		rule, ok := parseRule(args)
		if !ok {
			return invalidArgument
		}
		rule.ID = p.newRuleIDLocked()
		if !p.forgetful {
			p.rules = append(p.rules, rule)
		}
		return addRuleReply{ID: rule.ID}
	case "edit_rule":
		rule, ok := parseRule(args)
		if !ok {
			return invalidArgument
		}
		for i := range p.rules {
			if p.rules[i].ID == rule.ID {
				if !p.forgetful {
					p.rules[i] = rule
				}
				return status{}
			}
		}
		return invalidArgument
	case "delete_rule":
		var params struct {
			ID *string `json:"id"`
		}
		if err := json.Unmarshal(args, &params); err != nil || params.ID == nil {
			return invalidArgument
		}
		for i, rule := range p.rules {
			if rule.ID == *params.ID {
				p.rules = append(p.rules[:i:i], p.rules[i+1:]...)
				return status{}
			}
		}
		// What a real device says to an id it does not know has not been
		// seen. This is a guess.
		return invalidArgument
	default:
		return methodNotSupported
	}
}

type addRuleReply struct {
	ID        string `json:"id"`
	ErrorCode int    `json:"err_code"`
}

// parseRule reads the argument of add_rule or edit_rule. The mock keeps only
// the fields it reports, and insists on the ones a rule cannot do without.
func parseRule(args json.RawMessage) (Rule, bool) {
	var params struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Enable      *int   `json:"enable"`
		Repeat      *int   `json:"repeat"`
		Weekdays    []int  `json:"wday"`
		StartOption *int   `json:"stime_opt"`
		StartMinute *int   `json:"smin"`
		StartAction *int   `json:"sact"`
		EndAction   *int   `json:"eact"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return Rule{}, false
	}
	for _, field := range []*int{params.Enable, params.Repeat, params.StartOption, params.StartMinute, params.StartAction} {
		if field == nil {
			return Rule{}, false
		}
	}
	rule := Rule{
		ID:          params.ID,
		Name:        params.Name,
		Enable:      *params.Enable,
		Repeat:      *params.Repeat,
		StartOption: *params.StartOption,
		StartMinute: *params.StartMinute,
		StartAction: *params.StartAction,
		EndAction:   -1,
	}
	if params.EndAction != nil {
		rule.EndAction = *params.EndAction
	}
	if len(params.Weekdays) != len(rule.Weekdays) {
		return Rule{}, false
	}
	for day, flag := range params.Weekdays {
		if flag != 0 && flag != 1 {
			return Rule{}, false
		}
		rule.Weekdays[day] = flag
	}
	switch {
	case rule.Enable != 0 && rule.Enable != 1,
		rule.Repeat != 0 && rule.Repeat != 1,
		rule.StartOption < 0 || rule.StartOption > 2,
		rule.StartMinute < 0 || rule.StartMinute >= 24*60,
		rule.StartAction != 0 && rule.StartAction != 1,
		rule.EndAction < -1 || rule.EndAction > 1:
		return Rule{}, false
	}
	return rule, true
}

// newRuleIDLocked makes up an id for a new rule, in the form real devices
// use. The caller holds p.mu.
func (p *Plug) newRuleIDLocked() string {
	for {
		p.ruleIDs++
		id := fmt.Sprintf("%032X", 0xA000+p.ruleIDs)
		taken := false
		for _, rule := range p.rules {
			if rule.ID == id {
				taken = true
				break
			}
		}
		if !taken {
			return id
		}
	}
}

type rulesReply struct {
	Rules     []Rule `json:"rule_list"`
	Version   int    `json:"version"`
	Enable    int    `json:"enable"`
	ErrorCode int    `json:"err_code"`
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
