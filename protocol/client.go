package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// DefaultPort is the TCP port the protocol is served on.
const DefaultPort = 9999

// DefaultTimeout is how long a Client waits for one exchange when its
// Timeout field is zero.
const DefaultTimeout = 5 * time.Second

// Addr returns host joined with DefaultPort, in the form Send expects.
func Addr(host string) string {
	return net.JoinHostPort(host, strconv.Itoa(DefaultPort))
}

// Client sends requests to a device. The zero value is ready to use, and a
// Client is safe for concurrent use.
type Client struct {
	// Timeout bounds one whole exchange: connecting, sending the request
	// and reading the reply. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Send opens a connection to addr, sends request, and returns the reply.
//
// The request and the reply are JSON documents in plaintext; Send takes care
// of the encryption and framing. Each call uses its own connection, which is
// closed before Send returns.
//
// If ctx ends or the timeout passes first, the returned error wraps
// ctx.Err() or context.DeadlineExceeded.
func (c *Client) Send(ctx context.Context, addr string, request []byte) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("protocol: connect to %s: %w", addr, contextError(ctx, err))
	}
	defer conn.Close()

	// A context has no direct hold on a connection, so interrupt any
	// blocked read or write by moving the deadline into the past.
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Unix(1, 0))
	})
	defer stop()

	if err := WriteMessage(conn, request); err != nil {
		return nil, fmt.Errorf("protocol: send request to %s: %w", addr, contextError(ctx, err))
	}
	reply, err := ReadMessage(conn)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// A reply was expected, so a closed connection is an error.
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("protocol: read reply from %s: %w", addr, contextError(ctx, err))
	}
	return reply, nil
}

// contextError returns ctx's error if ctx has ended, and err otherwise. It
// turns the network timeout produced by an interrupted connection back into
// the reason for the interruption.
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
