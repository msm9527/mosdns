package cache

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestReadWALRecordRejectsOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], maxWALRecordPayloadLength+1)
	if _, err := buf.Write(size[:]); err != nil {
		t.Fatalf("write size header: %v", err)
	}

	_, err := readWALRecord(&buf)
	if err == nil {
		t.Fatal("expected oversized wal record to fail")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}
