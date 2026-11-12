// Package mockplug is an in-memory stand-in for a smart plug, for tests that
// should run without hardware.
//
// It listens on a loopback port and speaks the same wire protocol as a real
// device, but understands only a small set of commands. The device details
// it reports are placeholders, and its error replies are an approximation
// rather than a record of what any particular firmware sends.
package mockplug

import (
	"encoding/json"
	"errors"
	"net"
	"sync"

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

	mu      sync.Mutex
	relayOn bool
	closed  bool
	conns   map[net.Conn]struct{}
}

// New starts a mock plug on a free loopback port. Its relay starts off.
func New() (*Plug, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Plug{
		listener: listener,
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
	p.relayOn = on
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

	reply := make(map[string]any, len(modules))
	for module, methods := range modules {
		if module != "system" {
			reply[module] = errorReply{Code: errModuleNotSupported, Message: "module not support"}
			continue
		}
		results := make(map[string]any, len(methods))
		for method, args := range methods {
			results[method] = p.system(method, args)
		}
		reply[module] = results
	}

	encoded, err := json.Marshal(reply)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// system runs one method of the "system" module.
func (p *Plug) system(method string, args json.RawMessage) any {
	switch method {
	case "get_sysinfo":
		return p.sysinfo()
	case "set_relay_state":
		var params struct {
			State *int `json:"state"`
		}
		if err := json.Unmarshal(args, &params); err != nil || params.State == nil ||
			(*params.State != 0 && *params.State != 1) {
			return errorReply{Code: errInvalidArgument, Message: "invalid argument"}
		}
		p.SetRelay(*params.State == 1)
		return errorReply{Code: 0}
	default:
		return errorReply{Code: errMethodNotSupported, Message: "member not support"}
	}
}

// errorReply is the status object returned for a command. A zero code means
// success.
type errorReply struct {
	Code    int    `json:"err_code"`
	Message string `json:"err_msg,omitempty"`
}

// sysinfo is the reply to get_sysinfo. Every identifying value is a
// placeholder.
type sysinfo struct {
	SoftwareVersion string `json:"sw_ver"`
	HardwareVersion string `json:"hw_ver"`
	Model           string `json:"model"`
	DeviceID        string `json:"deviceId"`
	OEMID           string `json:"oemId"`
	HardwareID      string `json:"hwId"`
	RSSI            int    `json:"rssi"`
	Alias           string `json:"alias"`
	DeviceName      string `json:"dev_name"`
	MICType         string `json:"mic_type"`
	Feature         string `json:"feature"`
	MAC             string `json:"mac"`
	LEDOff          int    `json:"led_off"`
	RelayState      int    `json:"relay_state"`
	OnTime          int    `json:"on_time"`
	ErrorCode       int    `json:"err_code"`
}

func (p *Plug) sysinfo() sysinfo {
	relayState := 0
	if p.RelayOn() {
		relayState = 1
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
		DeviceName:      "Smart Wi-Fi Plug Mini",
		MICType:         "IOT.SMARTPLUGSWITCH",
		Feature:         "TIM",
		MAC:             "02:00:00:00:00:01",
		LEDOff:          0,
		RelayState:      relayState,
		OnTime:          0,
		ErrorCode:       0,
	}
}
