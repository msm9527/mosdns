package cache

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxWALRecordPayloadLength = dumpMaximumBlockLength

func readSizedBytes(r io.Reader, maxLength uint32, label string) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	payloadLength := binary.BigEndian.Uint32(size[:])
	if payloadLength > maxLength {
		return nil, fmt.Errorf("%s length %d exceeds limit %d", label, payloadLength, maxLength)
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
