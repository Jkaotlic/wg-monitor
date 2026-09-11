package checks

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// TestDoTFrameRejectsOversizedQuery: DNS-over-TCP (RFC 7858 → RFC 1035 §4.2.2)
// carries the message length in 2 bytes, so a message over 65535 bytes cannot
// be framed. It must be an error — never a length that silently wraps and
// desynchronises the stream.
func TestDoTFrameRejectsOversizedQuery(t *testing.T) {
	if frame, err := dotFrame(make([]byte, math.MaxUint16+1)); err == nil {
		t.Fatalf("a %d-byte message must not be framed, got a %d-byte frame with length prefix %d",
			math.MaxUint16+1, len(frame), binary.BigEndian.Uint16(frame))
	}

	largest, err := dotFrame(make([]byte, math.MaxUint16))
	if err != nil {
		t.Fatalf("65535 bytes is the largest valid DNS-over-TCP message: %v", err)
	}
	if got := binary.BigEndian.Uint16(largest); got != math.MaxUint16 || len(largest) != 2+math.MaxUint16 {
		t.Fatalf("largest frame: prefix %d, len %d; want prefix %d, len %d",
			got, len(largest), math.MaxUint16, 2+math.MaxUint16)
	}

	small, err := dotFrame([]byte{0xab, 0xcd, 0xef})
	if err != nil {
		t.Fatalf("small message: %v", err)
	}
	if want := []byte{0x00, 0x03, 0xab, 0xcd, 0xef}; !bytes.Equal(small, want) {
		t.Fatalf("frame = % x, want % x", small, want)
	}
}
