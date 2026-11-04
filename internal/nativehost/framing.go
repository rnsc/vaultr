// Package nativehost is vaultr's side of browser native messaging: the
// browser extension starts vaultr and talks to it over stdin and stdout.
//
// Each message is a 32-bit length in the machine's byte order followed by
// that many bytes of UTF-8 JSON. Browsers refuse messages over 1 MB from
// the host, so responses are kept under that.
package nativehost

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxOutgoing is the largest message a browser accepts from the host
// (1 MB in both Chrome and Firefox).
const MaxOutgoing = 1 << 20

// MaxIncoming caps what the host accepts. Browsers allow larger messages
// from the extension, but vaultr's requests are small: refusing big ones
// means a bad length never makes it allocate gigabytes.
const MaxIncoming = 1 << 20

// ErrTooLarge is returned for messages over the limits.
var ErrTooLarge = errors.New("message too large")

// ReadMsg reads one framed message. It returns io.EOF when the browser
// closed the pipe between messages.
func ReadMsg(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("reading message length: %w", err)
		}
		return nil, err // io.EOF: closed cleanly
	}
	n := binary.NativeEndian.Uint32(hdr[:])
	if n > MaxIncoming {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, n, MaxIncoming)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("reading message body: %w", err)
	}
	return buf, nil
}

// Encode marshals v and checks it fits in one message.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxOutgoing {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, len(b), MaxOutgoing)
	}
	return b, nil
}

// WriteMsg writes one framed message holding v as JSON.
func WriteMsg(w io.Writer, v any) error {
	b, err := Encode(v)
	if err != nil {
		return err
	}
	return writeRaw(w, b)
}

func writeRaw(w io.Writer, b []byte) error {
	// One Write call, so a message is never interleaved with another.
	out := make([]byte, 4+len(b))
	binary.NativeEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	_, err := w.Write(out)
	return err
}
