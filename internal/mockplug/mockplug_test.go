package mockplug_test

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/kasad/internal/mockplug"
	"github.com/gureetk/kasad/protocol"
)

func startPlug(t *testing.T) *mockplug.Plug {
	t.Helper()
	plug, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	t.Cleanup(func() { plug.Close() })
	return plug
}

// send performs one exchange and returns the reply's entry for each module.
func send(t *testing.T, addr, request string) map[string]json.RawMessage {
	t.Helper()
	var client protocol.Client
	raw, err := client.Send(context.Background(), addr, []byte(request))
	if err != nil {
		t.Fatalf("Send %s: %v", request, err)
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("decode reply %q: %v", raw, err)
	}
	return reply
}

// methods splits one module's entry into its per-method results.
func methods(t *testing.T, module json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var results map[string]json.RawMessage
	if err := json.Unmarshal(module, &results); err != nil {
		t.Fatalf("decode module entry %q: %v", module, err)
	}
	return results
}

func errCode(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var status struct {
		Code *int `json:"err_code"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status %q: %v", raw, err)
	}
	if status.Code == nil {
		t.Fatalf("status %q has no err_code", raw)
	}
	return *status.Code
}

func TestUnsupportedModule(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(), `{"emeter":{"get_realtime":{}}}`)
	if code := errCode(t, reply["emeter"]); code != -1 {
		t.Errorf("err_code = %d, want -1", code)
	}
}

func TestUnsupportedMethod(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(), `{"system":{"reboot":{"delay":1}}}`)
	if code := errCode(t, methods(t, reply["system"])["reboot"]); code != -2 {
		t.Errorf("err_code = %d, want -2", code)
	}
}

func TestSetRelayStateRejectsBadArguments(t *testing.T) {
	plug := startPlug(t)
	requests := []string{
		`{"system":{"set_relay_state":{}}}`,
		`{"system":{"set_relay_state":null}}`,
		`{"system":{"set_relay_state":{"state":2}}}`,
		`{"system":{"set_relay_state":{"state":"on"}}}`,
	}
	for _, request := range requests {
		reply := send(t, plug.Addr(), request)
		if code := errCode(t, methods(t, reply["system"])["set_relay_state"]); code != -3 {
			t.Errorf("%s: err_code = %d, want -3", request, code)
		}
	}
	if plug.RelayOn() {
		t.Error("relay turned on by a rejected request")
	}
}

func TestSeveralCommandsInOneRequest(t *testing.T) {
	plug := startPlug(t)
	reply := send(t, plug.Addr(),
		`{"system":{"set_relay_state":{"state":1},"get_sysinfo":{}},"time":{"get_time":null}}`)

	if code := errCode(t, methods(t, reply["system"])["set_relay_state"]); code != 0 {
		t.Errorf("set_relay_state err_code = %d, want 0", code)
	}
	if code := errCode(t, methods(t, reply["system"])["get_sysinfo"]); code != 0 {
		t.Errorf("get_sysinfo err_code = %d, want 0", code)
	}
	if code := errCode(t, reply["time"]); code != -1 {
		t.Errorf("unsupported module err_code = %d, want -1", code)
	}
	if !plug.RelayOn() {
		t.Error("relay is off after set_relay_state 1")
	}
}

func TestSeveralRequestsOnOneConnection(t *testing.T) {
	plug := startPlug(t)
	conn, err := net.Dial("tcp", plug.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	for i := 0; i < 3; i++ {
		if err := protocol.WriteMessage(conn, []byte(`{"system":{"get_sysinfo":{}}}`)); err != nil {
			t.Fatalf("request %d: write: %v", i, err)
		}
		if _, err := protocol.ReadMessage(conn); err != nil {
			t.Fatalf("request %d: read: %v", i, err)
		}
	}
}

func TestSetRelayDirectly(t *testing.T) {
	plug := startPlug(t)
	plug.SetRelay(true)

	reply := send(t, plug.Addr(), `{"system":{"get_sysinfo":{}}}`)
	var info struct {
		RelayState int `json:"relay_state"`
	}
	if err := json.Unmarshal(methods(t, reply["system"])["get_sysinfo"], &info); err != nil {
		t.Fatalf("decode sysinfo: %v", err)
	}
	if info.RelayState != 1 {
		t.Errorf("relay_state = %d, want 1", info.RelayState)
	}
}

func TestConcurrentClients(t *testing.T) {
	plug := startPlug(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var client protocol.Client
			request := `{"system":{"set_relay_state":{"state":0}}}`
			if i%2 == 0 {
				request = `{"system":{"set_relay_state":{"state":1}}}`
			}
			if _, err := client.Send(context.Background(), plug.Addr(), []byte(request)); err != nil {
				t.Errorf("client %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCloseDropsOpenConnections(t *testing.T) {
	plug, err := mockplug.New()
	if err != nil {
		t.Fatalf("start mock plug: %v", err)
	}
	conn, err := net.Dial("tcp", plug.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Make sure the plug has picked the connection up before closing it.
	if err := protocol.WriteMessage(conn, []byte(`{"system":{"get_sysinfo":{}}}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := protocol.ReadMessage(conn); err != nil {
		t.Fatalf("read: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- plug.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return while a client connection was open")
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := protocol.ReadMessage(conn); err == nil {
		t.Error("read succeeded on a connection the plug should have dropped")
	}
	if err := plug.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
