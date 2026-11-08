package protobuf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Wire types.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// ErrWire is returned for malformed protobuf wire data.
var ErrWire = errors.New("malformed protobuf message")

const (
	wireTypeMask  = 7
	fieldNumShift = 3
	maxVarintLen  = 10
	maxDepth      = 64
)

func appendVarint(b []byte, v uint64) []byte {
	return binary.AppendUvarint(b, v)
}

func appendTag(b []byte, num, wt int) []byte {
	return appendVarint(b, uint64(num)<<3|uint64(wt)) //nolint:gosec // field numbers are validated positive.
}

func appendBytes(b, data []byte) []byte {
	return append(appendVarint(b, uint64(len(data))), data...)
}

func appendFixed32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

func appendFixed64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

const (
	zz32Shift = 31
	zz64Shift = 63
)

//nolint:gosec // bit tricks.
func zigzag32(v int32) uint64 { return uint64(uint32(v<<1) ^ uint32(v>>zz32Shift)) }

//nolint:gosec // bit tricks.
func zigzag64(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>zz64Shift) }

//nolint:gosec // bit tricks.
func unzigzag(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }

// rawField is one decoded wire element.
type rawField struct {
	num  int
	wt   int
	v    uint64 // varint / fixed value.
	data []byte // length-delimited payload.
}

// readFields splits a message into its wire elements.
//
//nolint:cyclop // grammar and wire kinds map to branches
func readFields(b []byte) ([]rawField, error) {
	var out []rawField

	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, fmt.Errorf("%w: bad tag", ErrWire)
		}

		b = b[n:]
		//nolint:gosec // bounded by the check below.
		f := rawField{num: int(key >> fieldNumShift), wt: int(key & wireTypeMask), v: 0, data: nil}

		if f.num < 1 {
			return nil, fmt.Errorf("%w: field number 0", ErrWire)
		}

		switch f.wt {
		case wireVarint:
			f.v, n = binary.Uvarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("%w: bad varint", ErrWire)
			}

			b = b[n:]
		case wireFixed64:
			if len(b) < 8 { //nolint:mnd // 64 bits.
				return nil, fmt.Errorf("%w: truncated fixed64", ErrWire)
			}

			f.v, b = binary.LittleEndian.Uint64(b), b[8:]
		case wireFixed32:
			if len(b) < 4 { //nolint:mnd // 32 bits.
				return nil, fmt.Errorf("%w: truncated fixed32", ErrWire)
			}

			f.v, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		case wireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) { //nolint:gosec // wire-format conversion
				return nil, fmt.Errorf("%w: bad length", ErrWire)
			}

			f.data, b = b[n:n+int(l)], b[n+int(l):] //nolint:gosec // wire-format conversion
		default:
			return nil, fmt.Errorf("%w: unsupported wire type %d", ErrWire, f.wt)
		}

		out = append(out, f)
	}

	return out, nil
}

// readVarints reads a packed run of varints.
func readVarints(b []byte) ([]uint64, error) {
	var out []uint64

	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, fmt.Errorf("%w: bad packed varint", ErrWire)
		}

		out, b = append(out, v), b[n:]
	}

	return out, nil
}

func f32bits(f float32) uint32 { return math.Float32bits(f) }

func f64bits(f float64) uint64 { return math.Float64bits(f) }
