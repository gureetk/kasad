// Package plug provides typed commands for a smart plug on top of the wire
// protocol in package protocol.
package plug

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/gureetk/kasad/protocol"
)

// Plug is a handle on one device. Create one with New.
//
// A Plug is safe for concurrent use. It carries out one exchange at a time,
// so the device never has two requests to deal with at once.
type Plug struct {
	// Addr is the device's address as host:port.
	Addr string

	// Client performs the network exchanges. Its zero value uses the
	// default timeout. Set it up before the Plug is shared.
	Client protocol.Client

	// exchange is held for the length of each exchange with the device.
	exchange sync.Mutex
}

// New returns a Plug for the device at host. host is either a host name or
// IP address, which is given the default port, or an explicit host:port.
func New(host string) *Plug {
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No port. An IPv6 address may still be wrapped in brackets.
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		host = protocol.Addr(host)
	}
	return &Plug{Addr: host}
}

// Error is a failure reported by the device in its reply, as opposed to a
// network failure.
type Error struct {
	// Module and Method name the command the device rejected. Method is
	// empty when the device rejected the whole module.
	Module string
	Method string

	// Code and Message are the device's own error code and text.
	Code    int
	Message string
}

func (e *Error) Error() string {
	target := e.Module
	if e.Method != "" {
		target += "." + e.Method
	}
	if e.Message == "" {
		return fmt.Sprintf("plug: device rejected %s (code %d)", target, e.Code)
	}
	return fmt.Sprintf("plug: device rejected %s: %s (code %d)", target, e.Message, e.Code)
}

// command is one method call inside a request.
type command struct {
	module string
	method string
	params any // nil sends an empty object
	result any // pointer to decode the result into; nil discards it
}

// call sends all commands in a single request and decodes their results. A
// request is a JSON object of modules, each an object of methods, so several
// commands share one exchange.
func (p *Plug) call(ctx context.Context, commands ...command) error {
	request := make(map[string]map[string]any)
	for _, c := range commands {
		if request[c.module] == nil {
			request[c.module] = make(map[string]any)
		}
		params := c.params
		if params == nil {
			params = struct{}{}
		}
		request[c.module][c.method] = params
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("plug: encode request: %w", err)
	}

	p.exchange.Lock()
	raw, err := p.Client.Send(ctx, p.Addr, encoded)
	p.exchange.Unlock()
	if err != nil {
		return err
	}

	var reply map[string]json.RawMessage
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("plug: malformed reply: %w", err)
	}
	for _, c := range commands {
		if err := decodeResult(reply, c); err != nil {
			return err
		}
	}
	return nil
}

// decodeResult finds the result of c in reply, turns a device-reported
// failure into an *Error, and otherwise decodes the result into c.result.
func decodeResult(reply map[string]json.RawMessage, c command) error {
	moduleRaw, ok := reply[c.module]
	if !ok {
		return fmt.Errorf("plug: reply has no %q section", c.module)
	}
	var methods map[string]json.RawMessage
	if err := json.Unmarshal(moduleRaw, &methods); err != nil {
		return fmt.Errorf("plug: malformed %q section in reply: %w", c.module, err)
	}

	methodRaw, ok := methods[c.method]
	if !ok {
		// A module the device does not have is answered with a status in
		// place of the per-method results.
		if err := statusError(c.module, "", moduleRaw); err != nil {
			return err
		}
		return fmt.Errorf("plug: reply has no result for %s.%s", c.module, c.method)
	}
	if err := statusError(c.module, c.method, methodRaw); err != nil {
		return err
	}
	if c.result == nil {
		return nil
	}
	if err := json.Unmarshal(methodRaw, c.result); err != nil {
		return fmt.Errorf("plug: malformed result for %s.%s: %w", c.module, c.method, err)
	}
	return nil
}

// statusError returns an *Error if raw carries a non-zero err_code, and nil
// otherwise.
func statusError(module, method string, raw json.RawMessage) error {
	var status struct {
		Code    *int   `json:"err_code"`
		Message string `json:"err_msg"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("plug: malformed status for %s: %w", joinName(module, method), err)
	}
	if status.Code == nil || *status.Code == 0 {
		return nil
	}
	return &Error{Module: module, Method: method, Code: *status.Code, Message: status.Message}
}

func joinName(module, method string) string {
	if method == "" {
		return module
	}
	return module + "." + method
}
