package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
)

// ErrBadHistograms is returned for a histograms.bin that cannot be read.
var ErrBadHistograms = errors.New("malformed histograms file")

// maxEntryBytes bounds one entry when decoding, against corrupt files.
const maxEntryBytes = 1 << 20

// histogramSet collects the histograms of a run under stable names:
// "overall", "op:<name>", "step:<name>", "status:<code>", "phase:<name>".
func histogramSet(s *loadtest.Summary) map[string]*hdr.Histogram {
	out := map[string]*hdr.Histogram{"overall": s.Overall.Latency}

	for k, b := range s.Operations {
		out["op:"+k] = b.Latency
	}

	for k, b := range s.Steps {
		out["step:"+k] = b.Latency
	}

	for code, h := range s.Status {
		out[fmt.Sprintf("status:%d", code)] = h
	}

	for k, h := range s.Timings {
		out["phase:"+k] = h
	}

	return out
}

// EncodeHistograms writes histograms.bin: a count, then per histogram its
// name and its binary encoding, each length-prefixed. Names are sorted so the
// file is reproducible.
func EncodeHistograms(set map[string]*hdr.Histogram) ([]byte, error) {
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}

	sort.Strings(names)

	out := binary.AppendUvarint(nil, uint64(len(names)))

	for _, name := range names {
		data, err := set[name].MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("encode histogram %s: %w", name, err)
		}

		out = binary.AppendUvarint(out, uint64(len(name)))
		out = append(out, name...)
		out = binary.AppendUvarint(out, uint64(len(data)))
		out = append(out, data...)
	}

	return out, nil
}

// DecodeHistograms reads what EncodeHistograms wrote.
func DecodeHistograms(data []byte) (map[string]*hdr.Histogram, error) {
	n, rest, err := uvarint(data)
	if err != nil {
		return nil, err
	}

	out := make(map[string]*hdr.Histogram)

	for range n {
		var name, blob []byte

		name, rest, err = chunk(rest)
		if err != nil {
			return nil, err
		}

		blob, rest, err = chunk(rest)
		if err != nil {
			return nil, err
		}

		h := hdr.New()

		err = h.UnmarshalBinary(blob)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrBadHistograms, name, err)
		}

		out[string(name)] = h
	}

	return out, nil
}

func uvarint(b []byte) (v uint64, rest []byte, err error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, ErrBadHistograms
	}

	return v, b[n:], nil
}

// chunk reads one length-prefixed byte string.
func chunk(b []byte) (item, rest []byte, err error) {
	size, rest, err := uvarint(b)
	if err != nil {
		return nil, nil, err
	}

	if size > maxEntryBytes || size > uint64(len(rest)) {
		return nil, nil, ErrBadHistograms
	}

	return rest[:size], rest[size:], nil
}
