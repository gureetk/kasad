package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxMessageSize is the largest payload, in bytes, that WriteMessage will
// send and ReadMessage will accept. Real replies are a few kilobytes at most;
// the limit stops a corrupt or hostile length prefix from causing a huge
// allocation.
const MaxMessageSize = 1 << 20

// headerSize is the size of the length prefix that precedes every message
// sent over TCP.
const headerSize = 4

// ErrMessageTooLarge is returned when a payload is larger than
// MaxMessageSize.
var ErrMessageTooLarge = errors.New("protocol: message exceeds maximum size")

// WriteMessage encrypts payload and writes it to w, preceded by its length.
//
// The header and body are sent in a single Write call so that they reach the
// device together.
func WriteMessage(w io.Writer, payload []byte) error {
	if len(payload) > MaxMessageSize {
		return fmt.Errorf("%w: %d bytes", ErrMessageTooLarge, len(payload))
	}
	buf := make([]byte, headerSize+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	encryptInto(buf[headerSize:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadMessage reads one length-prefixed message from r and returns its
// decrypted payload.
//
// It returns io.EOF if r ends before any byte of a message arrives, and
// io.ErrUnexpectedEOF if r ends partway through one.
func ReadMessage(r io.Reader) ([]byte, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > MaxMessageSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrMessageTooLarge, size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			// The header promised a body, so ending here is never clean.
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	decryptInPlace(body)
	return body, nil
}
