package cache

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	walMagicV2             = "mosdns_cache_wal_v2\n"
	checkpointExtraVersion = byte(1)
)

type walCountingReader struct {
	r io.Reader
	n int64
}

func (r *walCountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

// base 和 cut 只累计 WAL record 字节，不包含任一版本的文件 header。
// suffix 轮换保持 generation，base 前移到已同步快照的 cut。
type walHeader struct {
	version    int
	generation [16]byte
	base       uint64
}

type snapshotCheckpoint struct {
	generation [16]byte
	cut        uint64
}

func newWALHeader() (walHeader, error) {
	header := walHeader{version: 2}
	_, err := rand.Read(header.generation[:])
	return header, err
}

func (header walHeader) size() int64 {
	if header.version == 1 {
		return int64(len(walMagic))
	}
	return int64(len(walMagicV2) + 16 + 8)
}

func readWALHeader(r io.Reader) (walHeader, error) {
	magic := make([]byte, len(walMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return walHeader{}, err
	}
	if string(magic) == walMagic {
		return walHeader{version: 1}, nil
	}
	if string(magic) != walMagicV2 {
		return walHeader{}, errors.New("invalid wal header")
	}
	header := walHeader{version: 2}
	var fields [24]byte
	if _, err := io.ReadFull(r, fields[:]); err != nil {
		return walHeader{}, err
	}
	copy(header.generation[:], fields[:16])
	header.base = binary.BigEndian.Uint64(fields[16:])
	if header.generation == [16]byte{} {
		return walHeader{}, errors.New("invalid wal generation")
	}
	return header, nil
}

func writeWALHeader(w io.Writer, header walHeader) error {
	if header.version != 2 || header.generation == [16]byte{} {
		return errors.New("invalid wal v2 header")
	}
	var fields [24]byte
	copy(fields[:16], header.generation[:])
	binary.BigEndian.PutUint64(fields[16:], header.base)
	if _, err := io.WriteString(w, walMagicV2); err != nil {
		return err
	}
	_, err := w.Write(fields[:])
	return err
}

func (checkpoint *snapshotCheckpoint) extra() []byte {
	if checkpoint == nil {
		return nil
	}
	// RFC 1952 Extra 子字段 MC，长度使用小端，内部整数使用大端。
	extra := make([]byte, 4+1+16+8)
	extra[0], extra[1] = 'M', 'C'
	binary.LittleEndian.PutUint16(extra[2:4], uint16(len(extra)-4))
	extra[4] = checkpointExtraVersion
	copy(extra[5:21], checkpoint.generation[:])
	binary.BigEndian.PutUint64(extra[21:], checkpoint.cut)
	return extra
}

func readSnapshotCheckpoint(extra []byte) (*snapshotCheckpoint, error) {
	var checkpoint *snapshotCheckpoint
	for len(extra) > 0 {
		if len(extra) < 4 {
			return nil, errors.New("invalid snapshot gzip extra")
		}
		size := int(binary.LittleEndian.Uint16(extra[2:4]))
		if size > len(extra)-4 {
			return nil, errors.New("truncated snapshot gzip extra")
		}
		if extra[0] == 'M' && extra[1] == 'C' {
			if checkpoint != nil || size != 25 || extra[4] != checkpointExtraVersion {
				return nil, errors.New("invalid snapshot checkpoint metadata")
			}
			checkpoint = new(snapshotCheckpoint)
			copy(checkpoint.generation[:], extra[5:21])
			checkpoint.cut = binary.BigEndian.Uint64(extra[21:29])
			if checkpoint.generation == [16]byte{} {
				return nil, errors.New("invalid snapshot generation")
			}
		}
		extra = extra[4+size:]
	}
	return checkpoint, nil
}

func walReplayOffset(header walHeader, checkpoint *snapshotCheckpoint, fileSize int64) (int64, error) {
	if fileSize < header.size() || uint64(fileSize-header.size()) > ^uint64(0)-header.base {
		return 0, errors.New("invalid wal logical size")
	}
	if checkpoint == nil {
		if header.version == 2 && header.base != 0 {
			return 0, errors.New("legacy snapshot cannot recover a rotated wal")
		}
		return header.size(), nil
	}
	if header.version != 2 || header.generation != checkpoint.generation {
		return 0, errors.New("snapshot and wal generations do not match")
	}
	if checkpoint.cut < header.base {
		return 0, errors.New("snapshot checkpoint precedes wal base")
	}
	if fileSize < header.size() || checkpoint.cut-header.base > uint64(fileSize-header.size()) {
		return 0, errors.New("snapshot checkpoint exceeds wal size")
	}
	return header.size() + int64(checkpoint.cut-header.base), nil
}

// 不应用快照已覆盖的前缀，仅读取 framing 验证 cut 处于完整记录边界。
func validateWALCut(r io.Reader, bytesToSkip int64) error {
	for bytesToSkip > 0 {
		if bytesToSkip < 4 {
			return errors.New("snapshot checkpoint splits a wal record")
		}
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return err
		}
		payload := int64(binary.BigEndian.Uint32(size[:]))
		if payload == 0 || payload > int64(maxWALRecordPayloadLength) || payload > bytesToSkip-4 {
			return fmt.Errorf("invalid wal record at snapshot checkpoint")
		}
		if _, err := io.CopyN(io.Discard, r, payload); err != nil {
			return err
		}
		bytesToSkip -= 4 + payload
	}
	return nil
}
