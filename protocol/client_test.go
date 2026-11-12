package protocol_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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

// sysinfoReply is the part of a get_sysinfo reply these tests look at.
type sysinfoReply struct {
	System struct {
		GetSysinfo struct {
			Model      string `json:"model"`
			RelayState int    `json:"relay_state"`
			ErrorCode  int    `json:"err_code"`
		} `json:"get_sysinfo"`
	} `json:"system"`
}

func getSysinfo(t *testing.T, client *protocol.Client, addr string) sysinfoReply {
	t.Helper()
	raw, err := client.Send(context.Background(), addr, []byte(`{"system":{"get_sysinfo":{}}}`))
	if err != nil {
		t.Fatalf("Send get_sysinfo: %v", err)
	}
	var reply sysinfoReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("decode reply %q: %v", raw, err)
	}
	return reply
}

func TestSendGetSysinfo(t *testing.T) {
	plug := startPlug(t)
	var client protocol.Client

	info := getSysinfo(t, &client, plug.Addr()).System.GetSysinfo
	if info.ErrorCode != 0 {
		t.Errorf("err_code = %d, want 0", info.ErrorCode)
	}
	if info.Model != "HS103(US)" {
		t.Errorf("model = %q, want %q", info.Model, "HS103(US)")
	}
	if info.RelayState != 0 {
		t.Errorf("relay_state = %d, want 0", info.RelayState)
	}
}

func TestSendSetRelayState(t *testing.T) {
	plug := startPlug(t)
	var client protocol.Client

	for _, state := range []int{1, 0, 1} {
		request, err := json.Marshal(map[string]any{
			"system": map[string]any{"set_relay_state": map[string]int{"state": state}},
		})
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		raw, err := client.Send(context.Background(), plug.Addr(), request)
		if err != nil {
			t.Fatalf("Send set_relay_state %d: %v", state, err)
		}
		var reply struct {
			System struct {
				SetRelayState struct {
					ErrorCode int `json:"err_code"`
				} `json:"set_relay_state"`
			} `json:"system"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("decode reply %q: %v", raw, err)
		}
		if code := reply.System.SetRelayState.ErrorCode; code != 0 {
			t.Errorf("set_relay_state %d: err_code = %d, want 0", state, code)
		}

		if got, want := plug.RelayOn(), state == 1; got != want {
			t.Errorf("after state %d: plug relay on = %v, want %v", state, got, want)
		}
		if got := getSysinfo(t, &client, plug.Addr()).System.GetSysinfo.RelayState; got != state {
			t.Errorf("after state %d: reported relay_state = %d", state, got)
		}
	}
}

// silentListener accepts connections and never answers them.
func silentListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				conn.Close()
			}()
		}
	}()
	return listener.Addr().String()
}

func TestSendTimesOut(t *testing.T) {
	addr := silentListener(t)
	client := protocol.Client{Timeout: 100 * time.Millisecond}

	start := time.Now()
	_, err := client.Send(context.Background(), addr, []byte(`{"system":{"get_sysinfo":{}}}`))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send took %v to give up on a 100ms timeout", elapsed)
	}
}

func TestSendStopsWhenContextIsCanceled(t *testing.T) {
	addr := silentListener(t)
	client := protocol.Client{Timeout: 30 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := client.Send(ctx, addr, []byte(`{"system":{"get_sysinfo":{}}}`))
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send error = %v, want context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send took %v to notice the canceled context", elapsed)
	}
}

func TestSendConnectionRefused(t *testing.T) {
	// Take a free port, then release it so that nothing is listening there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	var client protocol.Client
	if _, err := client.Send(context.Background(), addr, []byte(`{}`)); err == nil {
		t.Error("Send to a closed port succeeded, want an error")
	}
}

func TestSendConnectionClosedBeforeReply(t *testing.T) {
	plug := startPlug(t)
	var client protocol.Client

	// The mock plug hangs up on anything that is not a JSON object.
	_, err := client.Send(context.Background(), plug.Addr(), []byte(`not json`))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Send error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestAddr(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":   "192.0.2.10:9999",
		"plug.example": "plug.example:9999",
		"2001:db8::1":  "[2001:db8::1]:9999",
	}
	for host, want := range cases {
		if got := protocol.Addr(host); got != want {
			t.Errorf("Addr(%q) = %q, want %q", host, got, want)
		}
	}
}
