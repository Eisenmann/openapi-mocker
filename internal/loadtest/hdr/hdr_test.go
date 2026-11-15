package hdr_test

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/loadtest/hdr"
)

func exactPercentile(sorted []int64, p float64) int64 {
	rank := max(int(math.Ceil(p/100*float64(len(sorted)))), 1)

	return sorted[rank-1]
}

func relErr(got, want int64) float64 {
	return math.Abs(float64(got)-float64(want)) / float64(want)
}

func TestPercentilesMatchExactValues(t *testing.T) {
	t.Parallel()

	const samples = 1_000_000

	rng := rand.New(rand.NewPCG(1, 2))
	h := hdr.New()
	exact := make([]int64, samples)

	for i := range exact {
		// lognormal latencies around 20 ms with a long tail, in microseconds
		v := int64(math.Exp(rng.NormFloat64()*1.2+math.Log(20_000))) + 1
		v = min(v, hdr.HighestValue)
		exact[i] = v

		h.Record(v)
	}

	slices.Sort(exact)

	if h.Count() != samples {
		t.Fatalf("count %d", h.Count())
	}

	for _, p := range []float64{1, 10, 25, 50, 75, 90, 95, 99, 99.9, 99.99} {
		got, want := h.ValueAtPercentile(p), exactPercentile(exact, p)
		if relErr(got, want) > 0.001 {
			t.Errorf("p%v: got %d want %d (error %.4f%%)", p, got, want, relErr(got, want)*100)
		}
	}

	if h.Min() != exact[0] || h.Max() != exact[samples-1] {
		t.Errorf("min/max %d/%d want %d/%d", h.Min(), h.Max(), exact[0], exact[samples-1])
	}

	if h.ValueAtPercentile(0) != exact[0] || h.ValueAtPercentile(100) != exact[samples-1] {
		t.Error("p0 and p100 must be min and max")
	}
}

func TestMeanAndStdDev(t *testing.T) {
	t.Parallel()

	h := hdr.New()
	for _, v := range []int64{100, 200, 300, 400, 500} {
		h.Record(v)
	}

	if h.Mean() != 300 {
		t.Errorf("mean %v", h.Mean())
	}

	if sd := h.StdDev(); math.Abs(sd-math.Sqrt(20000)) > 1 {
		t.Errorf("stddev %v", sd)
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()

	h := hdr.New()
	if h.Count() != 0 || h.Min() != 0 || h.Max() != 0 || h.Mean() != 0 || h.StdDev() != 0 || h.ValueAtPercentile(50) != 0 {
		t.Error("an empty histogram reports zeros")
	}

	h.Iterate(func(hdr.Bucket) bool { t.Error("nothing to iterate"); return true })
}

func TestClampingAndRecordN(t *testing.T) {
	t.Parallel()

	h := hdr.New()
	h.Record(-5)
	h.Record(0)
	h.Record(hdr.HighestValue * 10)
	h.RecordN(1000, 3)
	h.RecordN(1000, 0)
	h.RecordDuration(2 * time.Millisecond)

	if h.Count() != 7 || h.Min() != 1 || h.Max() != hdr.HighestValue {
		t.Errorf("count %d min %d max %d", h.Count(), h.Min(), h.Max())
	}

	if got := h.ValueAtPercentile(80); relErr(got, 2000) > 0.001 {
		t.Errorf("p80 %d", got)
	}
}

func TestMergeIsLossless(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(3, 4))
	a, b, all := hdr.New(), hdr.New(), hdr.New()

	for i := range 200_000 {
		v := rng.Int64N(5_000_000) + 1
		all.Record(v)

		if i%3 == 0 {
			a.Record(v)
		} else {
			b.Record(v)
		}
	}

	a.Merge(b)
	a.Merge(nil)
	a.Merge(hdr.New())

	if a.Count() != all.Count() || a.Min() != all.Min() || a.Max() != all.Max() || a.Mean() != all.Mean() {
		t.Fatal("totals differ after merge")
	}

	for _, p := range []float64{1, 50, 90, 99, 99.9} {
		if a.ValueAtPercentile(p) != all.ValueAtPercentile(p) {
			t.Errorf("p%v differs after merge", p)
		}
	}
}

func TestIterateGivesCDF(t *testing.T) {
	t.Parallel()

	h := hdr.New()
	for v := int64(1); v <= 1000; v++ {
		h.Record(v * 100)
	}

	var last hdr.Bucket

	var n int

	h.Iterate(func(b hdr.Bucket) bool {
		if b.Value <= last.Value || b.Cumulative != last.Cumulative+b.Count {
			t.Fatalf("buckets must ascend and accumulate: %+v after %+v", b, last)
		}

		last = b
		n++

		return true
	})

	if last.Cumulative != 1000 || n == 0 {
		t.Errorf("cumulative %d in %d buckets", last.Cumulative, n)
	}

	stopped := 0

	h.Iterate(func(hdr.Bucket) bool { stopped++; return false })

	if stopped != 1 {
		t.Error("Iterate must stop when fn returns false")
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	h := hdr.New()
	h.Record(5000)
	h.Reset()

	if h.Count() != 0 || h.Max() != 0 || h.Min() != 0 {
		t.Error("reset must empty the histogram")
	}

	h.Record(7)

	if h.Min() != 7 || h.Max() != 7 {
		t.Error("a reset histogram must record normally")
	}
}

func TestBinaryRoundTrip(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(5, 6))
	h := hdr.New()

	for range 50_000 {
		h.Record(rng.Int64N(3_000_000) + 1)
	}

	data, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	if len(data) > 40_000 {
		t.Errorf("sparse encoding is %d bytes", len(data))
	}

	got := hdr.New()
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}

	if got.Count() != h.Count() || got.Min() != h.Min() || got.Max() != h.Max() || got.Mean() != h.Mean() ||
		got.ValueAtPercentile(99) != h.ValueAtPercentile(99) {
		t.Error("round trip changed the histogram")
	}

	empty, _ := hdr.New().MarshalBinary()
	if err := got.UnmarshalBinary(empty); err != nil || got.Count() != 0 || got.Min() != 0 {
		t.Errorf("empty round trip: %v", err)
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	t.Parallel()

	good, _ := func() ([]byte, error) {
		h := hdr.New()
		h.Record(1234)

		return h.MarshalBinary()
	}()

	cases := map[string][]byte{
		"empty":     nil,
		"bad magic": []byte("NOPE\x00\x00\x00\x00\x00\x00"),
		"truncated": good[:len(good)-1],
		"trailing":  append(slices.Clone(good), 0),
		"total off": append(slices.Clone(good[:4]), 9, 9, 9, 9, 0),
	}

	for name, data := range cases {
		if err := hdr.New().UnmarshalBinary(data); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func BenchmarkRecord(b *testing.B) {
	h := hdr.New()
	vals := make([]int64, 1024)
	rng := rand.New(rand.NewPCG(7, 8))

	for i := range vals {
		vals[i] = rng.Int64N(2_000_000) + 1
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := range b.N {
		h.Record(vals[i&1023])
	}
}
