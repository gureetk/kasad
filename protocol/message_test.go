package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestWriteMessageLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, []byte(sysinfoRequest)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	// 29 bytes of payload, so the length prefix is 0x0000001d.
	want := append([]byte{0x00, 0x00, 0x00, 0x1D}, sysinfoCiphertext(t)...)
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("wire bytes\n got %x\nwant %x", buf.Bytes(), want)
	}
}

// countingWriter records how many times Write is called.
type countingWriter struct {
	calls int
	bytes.Buffer
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.calls++
	return w.Buffer.Write(p)
}

func TestWriteMessageUsesOneWrite(t *testing.T) {
	var w countingWriter
	if err := WriteMessage(&w, []byte(sysinfoRequest)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if w.calls != 1 {
		t.Errorf("WriteMessage made %d Write calls, want 1", w.calls)
	}
}

func TestWriteMessageTooLarge(t *testing.T) {
	var buf bytes.Buffer
	err := WriteMessage(&buf, make([]byte, MaxMessageSize+1))
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Errorf("WriteMessage error = %v, want ErrMessageTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("WriteMessage wrote %d bytes before failing", buf.Len())
	}
}

func TestMessageRoundTrip(t *testing.T) {
	payloads := [][]byte{
		[]byte(sysinfoRequest),
		{},
		make([]byte, MaxMessageSize),
	}

	var buf bytes.Buffer
	for _, payload := range payloads {
		if err := WriteMessage(&buf, payload); err != nil {
			t.Fatalf("WriteMessage(%d bytes): %v", len(payload), err)
		}
	}
	// Messages written back to back must come out one at a time, intact.
	for _, want := range payloads {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage(%d bytes): %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadMessage returned %d bytes that differ from the %d written", len(got), len(want))
		}
	}
	if _, err := ReadMessage(&buf); err != io.EOF {
		t.Errorf("ReadMessage on drained stream = %v, want io.EOF", err)
	}
}

func TestReadMessageErrors(t *testing.T) {
	var whole bytes.Buffer
	if err := WriteMessage(&whole, []byte(sysinfoRequest)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	wire := whole.Bytes()

	cases := []struct {
		name  string
		input []byte
		want  error
	}{
		{"empty stream", nil, io.EOF},
		{"partial header", wire[:2], io.ErrUnexpectedEOF},
		{"header only", wire[:headerSize], io.ErrUnexpectedEOF},
		{"partial body", wire[:len(wire)-1], io.ErrUnexpectedEOF},
		{"length over limit", []byte{0xFF, 0xFF, 0xFF, 0xFF}, ErrMessageTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadMessage(bytes.NewReader(tc.input))
			if !errors.Is(err, tc.want) {
				t.Errorf("ReadMessage error = %v, want %v", err, tc.want)
			}
		})
	}
}
