package nativehost

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func frame(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.NativeEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

func TestFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, v := range []any{map[string]string{"a": "b"}, []int{1, 2, 3}, "é ü 日本"} {
		if err := WriteMsg(&buf, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{`{"a":"b"}`, `[1,2,3]`, `"é ü 日本"`} {
		got, err := ReadMsg(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if _, err := ReadMsg(&buf); err != io.EOF {
		t.Errorf("after the last message: %v, want io.EOF", err)
	}
}

func TestFramingNativeByteOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, "x"); err != nil {
		t.Fatal(err)
	}
	if got := binary.NativeEndian.Uint32(buf.Bytes()[:4]); got != 3 {
		t.Errorf("length prefix %d, want 3", got)
	}
}

func TestFramingZeroLength(t *testing.T) {
	got, err := ReadMsg(bytes.NewReader(frame(nil)))
	if err != nil || len(got) != 0 {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestFramingShortReads(t *testing.T) {
	full := frame([]byte(`{"id":1}`))
	for _, n := range []int{2, 4, 7} {
		_, err := ReadMsg(bytes.NewReader(full[:n]))
		if err == nil || err == io.EOF {
			t.Errorf("truncated at %d: %v, want an error other than EOF", n, err)
		}
	}
	// A reader returning one byte at a time still reads whole messages.
	got, err := ReadMsg(&oneByte{bytes.NewReader(full)})
	if err != nil || string(got) != `{"id":1}` {
		t.Errorf("one byte at a time: %q, %v", got, err)
	}
}

type oneByte struct{ r io.Reader }

func (o *oneByte) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return o.r.Read(p)
}

func TestFramingLimits(t *testing.T) {
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], MaxIncoming+1)
	if _, err := ReadMsg(bytes.NewReader(hdr[:])); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized incoming: %v", err)
	}
	binary.NativeEndian.PutUint32(hdr[:], 0xFFFFFFFF)
	if _, err := ReadMsg(bytes.NewReader(hdr[:])); !errors.Is(err, ErrTooLarge) {
		t.Errorf("4 GB length: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteMsg(&buf, strings.Repeat("a", MaxOutgoing)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized outgoing: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("an oversized message was partly written")
	}
	if err := WriteMsg(&buf, strings.Repeat("a", MaxOutgoing-2)); err != nil {
		t.Errorf("message of exactly the limit: %v", err)
	}
}
