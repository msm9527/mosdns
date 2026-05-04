package cache

import "encoding/binary"

func findResponseTTLOffsets(wire []byte) []int {
	if len(wire) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(wire[4:6]))
	an := int(binary.BigEndian.Uint16(wire[6:8]))
	ns := int(binary.BigEndian.Uint16(wire[8:10]))
	ar := int(binary.BigEndian.Uint16(wire[10:12]))

	off := 12
	for i := 0; i < qd; i++ {
		next, ok := skipWireName(wire, off)
		if !ok || next+4 > len(wire) {
			return nil
		}
		off = next + 4
	}

	totalRR := an + ns + ar
	offsets := make([]int, 0, totalRR)
	for i := 0; i < totalRR; i++ {
		next, ok := skipWireName(wire, off)
		if !ok || next+10 > len(wire) {
			return offsets
		}
		ttlOffset := next + 4
		rdLenOffset := next + 8
		rdLen := int(binary.BigEndian.Uint16(wire[rdLenOffset : rdLenOffset+2]))
		off = next + 10 + rdLen
		if off > len(wire) {
			return offsets
		}
		offsets = append(offsets, ttlOffset)
	}
	return offsets
}

func skipWireName(wire []byte, off int) (int, bool) {
	if off < 0 || off >= len(wire) {
		return 0, false
	}
	for {
		if off >= len(wire) {
			return 0, false
		}
		l := int(wire[off])
		switch l & 0xC0 {
		case 0x00:
			off++
			if l == 0 {
				return off, true
			}
			off += l
			if off > len(wire) {
				return 0, false
			}
		case 0xC0:
			if off+2 > len(wire) {
				return 0, false
			}
			return off + 2, true
		default:
			return 0, false
		}
	}
}
