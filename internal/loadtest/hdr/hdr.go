// Package hdr is a fixed-range high dynamic range histogram for latencies.
//
// It follows the HdrHistogram layout: values from 1 microsecond to 60 seconds
// are kept with 3 significant decimal digits (a relative error below 0.1%) in
// a few thousand counters, so recording is a couple of bit operations and one
// increment, and histograms of the same layout merge without losing anything.
//
// A Histogram is not safe for concurrent use. The intended pattern is one
// histogram per worker, merged by the collector when a bucket is closed, so
// the hot path needs no locks.
package hdr

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"time"
)

// Layout of every histogram: the unit is one microsecond.
const (
	// LowestValue is the smallest value that is told apart from zero (1 us).
	LowestValue int64 = 1
	// HighestValue is the largest value kept (60 s in microseconds); larger
	// values are recorded as HighestValue.
	HighestValue int64 = 60_000_000
	// SignificantDigits is the decimal precision of every recorded value.
	SignificantDigits = 3

	// subBucketCountMagnitude: 2^11 = 2048 sub-buckets cover the 2*10^3 values
	// that need unit resolution for 3 significant digits.
	subBucketCountMagnitude = 11
	subBucketCount          = 1 << subBucketCountMagnitude
	subBucketHalfCountMag   = subBucketCountMagnitude - 1
	subBucketHalfCount      = subBucketCount / 2
	subBucketMask           = int64(subBucketCount - 1)

	percentMax = 100.0
	headerSize = 4
	version    = 1
)

var magic = [headerSize]byte{'H', 'D', 'R', version}

// ErrCorrupt is returned by UnmarshalBinary for data that is not a histogram
// produced by MarshalBinary.
var ErrCorrupt = errors.New("hdr: corrupt histogram data")

// bucketsNeeded is the number of exponential buckets that reach HighestValue.
func bucketsNeeded() int {
	smallestUntrackable := int64(subBucketCount)
	n := 1

	for smallestUntrackable <= HighestValue {
		smallestUntrackable <<= 1
		n++
	}

	return n
}

var countsLen = (bucketsNeeded() + 1) * subBucketHalfCount

// Histogram counts values per bucket.
type Histogram struct {
	counts []int64
	total  int64
	sum    int64
	min    int64
	max    int64
}

// New returns an empty histogram. It allocates all counters up front (about
// 130 KB); Record never allocates.
func New() *Histogram {
	h := new(Histogram)
	h.counts = make([]int64, countsLen)
	h.min = math.MaxInt64

	return h
}

// Reset forgets everything that was recorded.
func (h *Histogram) Reset() {
	clear(h.counts)
	h.total = 0
	h.sum = 0
	h.min = math.MaxInt64
	h.max = 0
}

func clampValue(v int64) int64 {
	switch {
	case v < LowestValue:
		return LowestValue
	case v > HighestValue:
		return HighestValue
	default:
		return v
	}
}

// The conversions below are safe by construction: values are clamped to
// 1..HighestValue and indexes stay inside the counter array.

func u64(v int64) uint64 {
	return uint64(max(v, 0)) //nolint:gosec // clamped to non-negative
}

func i64(v uint64) int64 {
	return int64(min(v, math.MaxInt64)) //nolint:gosec // clamped to the int64 range
}

func u64FromInt(v int) uint64 {
	return uint64(max(v, 0)) //nolint:gosec // clamped to non-negative
}

func intFrom(v uint64) int {
	return int(min(v, math.MaxInt32)) //nolint:gosec // clamped to a range every int holds
}

func shiftOf(n int) uint {
	return uint(max(n, 0)) //nolint:gosec // clamped to non-negative
}

// bucketIndexOf is the exponential bucket a value falls into.
func bucketIndexOf(v int64) int {
	pow2ceiling := bits.Len64(u64(v | subBucketMask))

	return pow2ceiling - subBucketCountMagnitude
}

func countsIndexOf(v int64) int {
	b := bucketIndexOf(v)
	sub := int(v >> shiftOf(b))

	return (b+1)<<subBucketHalfCountMag + (sub - subBucketHalfCount)
}

// valueFromIndex is the lowest value that maps to the counter.
func valueFromIndex(index int) int64 {
	b := (index >> subBucketHalfCountMag) - 1
	sub := (index & (subBucketHalfCount - 1)) + subBucketHalfCount

	if b < 0 {
		sub -= subBucketHalfCount
		b = 0
	}

	return int64(sub) << shiftOf(b)
}

// equivalentRange is how many distinct values share the counter of v.
func equivalentRange(v int64) int64 {
	b := bucketIndexOf(v)
	if int(v>>shiftOf(b)) >= subBucketCount {
		b++
	}

	return int64(1) << shiftOf(b)
}

// midpoint is the value reported for a counter: the middle of its range.
func midpoint(index int) int64 {
	lowest := valueFromIndex(index)

	return lowest + equivalentRange(lowest)>>1
}

// Record adds one value in microseconds. Values below 1 count as 1 and values
// above HighestValue count as HighestValue.
func (h *Histogram) Record(v int64) {
	h.RecordN(v, 1)
}

// RecordN adds n occurrences of a value in microseconds.
func (h *Histogram) RecordN(v, n int64) {
	if n <= 0 {
		return
	}

	v = clampValue(v)
	h.counts[countsIndexOf(v)] += n
	h.total += n
	h.sum += v * n

	if v < h.min {
		h.min = v
	}

	if v > h.max {
		h.max = v
	}
}

// RecordDuration adds a latency.
func (h *Histogram) RecordDuration(d time.Duration) {
	h.Record(d.Microseconds())
}

// Merge adds everything in other to h. Both use the same layout, so the
// result is exactly what recording all values in one histogram would give.
func (h *Histogram) Merge(other *Histogram) {
	if other == nil || other.total == 0 {
		return
	}

	for i, c := range other.counts {
		h.counts[i] += c
	}

	h.total += other.total
	h.sum += other.sum
	h.min = min(h.min, other.min)
	h.max = max(h.max, other.max)
}

// Count is the number of recorded values.
func (h *Histogram) Count() int64 { return h.total }

// Min is the smallest recorded value, 0 when empty.
func (h *Histogram) Min() int64 {
	if h.total == 0 {
		return 0
	}

	return h.min
}

// Max is the largest recorded value, 0 when empty.
func (h *Histogram) Max() int64 { return h.max }

// Mean is the exact average of the recorded values, 0 when empty.
func (h *Histogram) Mean() float64 {
	if h.total == 0 {
		return 0
	}

	return float64(h.sum) / float64(h.total)
}

// StdDev is the standard deviation, with every value taken at the middle of
// its counter.
func (h *Histogram) StdDev() float64 {
	if h.total == 0 {
		return 0
	}

	mean := h.Mean()
	acc := 0.0

	for i, c := range h.counts {
		if c == 0 {
			continue
		}

		d := float64(midpoint(i)) - mean
		acc += d * d * float64(c)
	}

	return math.Sqrt(acc / float64(h.total))
}

// ValueAtPercentile returns the value below which p percent of the recorded
// values fall (p between 0 and 100), within 0.1% of the exact value. An empty
// histogram gives 0.
func (h *Histogram) ValueAtPercentile(p float64) int64 {
	if h.total == 0 {
		return 0
	}

	switch {
	case p <= 0:
		return h.min
	case p >= percentMax:
		return h.max
	}

	target := max(int64(math.Ceil(p/percentMax*float64(h.total))), 1)

	var seen int64

	for i, c := range h.counts {
		seen += c
		if c != 0 && seen >= target {
			return min(max(midpoint(i), h.min), h.max)
		}
	}

	return h.max
}

// Bucket is one step of Iterate.
type Bucket struct {
	// Value is the middle of the counter's value range, in microseconds.
	Value int64
	// Count is how many values fell into the counter.
	Count int64
	// Cumulative is the number of values up to and including this counter.
	Cumulative int64
}

// Iterate calls fn for every counter that holds values, from the smallest to
// the largest, until fn returns false. It is meant for histogram and CDF
// output.
func (h *Histogram) Iterate(fn func(b Bucket) bool) {
	var seen int64

	for i, c := range h.counts {
		if c == 0 {
			continue
		}

		seen += c

		var b Bucket

		b.Value = midpoint(i)
		b.Count = c
		b.Cumulative = seen

		if !fn(b) {
			return
		}
	}
}

// MarshalBinary encodes the histogram sparsely: only non-empty counters are
// written, so a typical histogram takes a few hundred bytes.
func (h *Histogram) MarshalBinary() ([]byte, error) {
	out := make([]byte, 0, headerSize+binary.MaxVarintLen64*6)
	out = append(out, magic[:]...)
	out = binary.AppendUvarint(out, u64(h.total))
	out = binary.AppendUvarint(out, u64(h.sum))
	out = binary.AppendUvarint(out, u64(h.Min()))
	out = binary.AppendUvarint(out, u64(h.max))

	nonEmpty := 0

	for _, c := range h.counts {
		if c != 0 {
			nonEmpty++
		}
	}

	out = binary.AppendUvarint(out, u64FromInt(nonEmpty))
	prev := 0

	for i, c := range h.counts {
		if c == 0 {
			continue
		}

		out = binary.AppendUvarint(out, u64FromInt(i-prev))
		out = binary.AppendUvarint(out, u64(c))
		prev = i
	}

	return out, nil
}

type reader struct {
	data []byte
	err  error
}

func (r *reader) next() uint64 {
	if r.err != nil {
		return 0
	}

	v, n := binary.Uvarint(r.data)
	if n <= 0 {
		r.err = ErrCorrupt

		return 0
	}

	r.data = r.data[n:]

	return v
}

// UnmarshalBinary replaces the histogram with one produced by MarshalBinary.
// h must come from New.
func (h *Histogram) UnmarshalBinary(data []byte) error {
	if len(h.counts) != countsLen || len(data) < headerSize || [headerSize]byte(data[:headerSize]) != magic {
		return ErrCorrupt
	}

	r := &reader{data: data[headerSize:], err: nil}
	total, sum, lo, hi, nonEmpty := r.next(), r.next(), r.next(), r.next(), r.next()

	if r.err != nil || nonEmpty > u64FromInt(countsLen) {
		return ErrCorrupt
	}

	counts, seen := r.counts(nonEmpty)

	if r.err != nil || seen != total || len(r.data) != 0 || max(total, sum, hi, lo) > math.MaxInt64 {
		return ErrCorrupt
	}

	h.set(counts, total, sum, lo, hi)

	return nil
}

func (h *Histogram) set(counts []int64, total, sum, lo, hi uint64) {
	h.counts = counts
	h.total = i64(total)
	h.sum = i64(sum)
	h.max = i64(hi)
	h.min = i64(lo)

	if total == 0 {
		h.min = math.MaxInt64
	}
}

// counts reads nonEmpty (index delta, count) pairs into a counter array and
// returns it with the sum of the counts.
func (r *reader) counts(nonEmpty uint64) (counts []int64, seen uint64) {
	counts = make([]int64, countsLen)
	index := 0

	for range nonEmpty {
		delta, c := r.next(), r.next()
		if r.err != nil || delta > u64FromInt(countsLen) || c == 0 || c > math.MaxInt64 {
			r.err = ErrCorrupt

			return nil, 0
		}

		index += intFrom(delta)
		if index >= countsLen {
			r.err = ErrCorrupt

			return nil, 0
		}

		counts[index] = i64(c)
		seen += c
	}

	return counts, seen
}
