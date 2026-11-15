package loadtest_test

import (
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
)

func rows(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"i": i}
	}

	return out
}

func TestFeeder_Modes(t *testing.T) {
	t.Parallel()

	seq, _ := loadtest.NewFeeder("s", "", rows(3))

	var got []int

	for range 7 {
		r, ok := seq.Next()
		if !ok {
			t.Fatal("sequential never runs out")
		}

		got = append(got, r["i"].(int))
	}

	if want := []int{0, 1, 2, 0, 1, 2, 0}; len(got) != 7 || got[3] != want[3] || got[6] != want[6] || got[2] != 2 {
		t.Errorf("sequential order %v", got)
	}

	uniq, _ := loadtest.NewFeeder("u", domain.FeederUnique, rows(3))
	for i := range 3 {
		if r, ok := uniq.Next(); !ok || r["i"] != i {
			t.Fatalf("unique row %d: %v %v", i, r, ok)
		}
	}

	if uniq.Exhausted() {
		t.Error("not exhausted before the next call")
	}

	if _, ok := uniq.Next(); ok || !uniq.Exhausted() {
		t.Error("a unique feeder must run out and say so")
	}

	rnd, _ := loadtest.NewFeeder("r", domain.FeederRandom, rows(5))
	seen := map[int]bool{}

	for range 200 {
		r, _ := rnd.Next()
		seen[r["i"].(int)] = true
	}

	if len(seen) != 5 {
		t.Errorf("random reached %d of 5 rows", len(seen))
	}

	if rnd.Name() != "r" || rnd.Rows() != 5 {
		t.Error("name and size")
	}
}

func TestFeeder_UniqueIsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()

	f, _ := loadtest.NewFeeder("u", domain.FeederUnique, rows(1000))

	var mu sync.Mutex

	seen := map[int]int{}

	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				r, ok := f.Next()
				if !ok {
					return
				}

				mu.Lock()
				seen[r["i"].(int)]++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(seen) != 1000 {
		t.Fatalf("%d distinct rows", len(seen))
	}

	for i, n := range seen {
		if n != 1 {
			t.Fatalf("row %d handed out %d times", i, n)
		}
	}
}

func TestNewFeeder_Empty(t *testing.T) {
	t.Parallel()

	if _, err := loadtest.NewFeeder("x", "", nil); !errors.Is(err, loadtest.ErrFeederEmpty) {
		t.Errorf("got %v", err)
	}
}

func TestLoadFeeders(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"users.csv":  {Data: []byte("email, password\nann@x.io,p1\nbob@x.io,\"p,2\"\n")},
		"ids.json":   {Data: []byte(`[{"id": 7, "tags": {"a": "x"}}, {"id": 8}]`)},
		"plain.json": {Data: []byte(`["a", "b"]`)},
		"header.csv": {Data: []byte("only,header\n")},
		"ragged.csv": {Data: []byte("a,b\n1\n")},
		"bad.json":   {Data: []byte(`{"not": "a list"}`)},
		"empty.json": {Data: []byte(`[]`)},
	}

	got, err := loadtest.LoadFeeders(fsys, []domain.DataFeeder{
		{Name: "users", File: "users.csv", Format: domain.FeederCSV, Mode: domain.FeederUnique},
		{Name: "ids", File: "ids.json", Format: domain.FeederJSON},
		{Name: "plain", File: "plain.json", Format: domain.FeederJSON},
	})
	if err != nil {
		t.Fatal(err)
	}

	r, _ := got["users"].Next()
	if r["email"] != "ann@x.io" || r["password"] != "p1" {
		t.Errorf("csv row: %v (the header is trimmed)", r)
	}

	r, _ = got["users"].Next()
	if r["password"] != "p,2" {
		t.Errorf("quoted csv field: %v", r)
	}

	r, _ = got["ids"].Next()
	if r["id"].(interface{ String() string }).String() != "7" || r["tags"].(map[string]any)["a"] != "x" {
		t.Errorf("json row: %#v", r)
	}

	if r, _ = got["plain"].Next(); r["value"] != "a" {
		t.Errorf("plain values: %v", r)
	}

	bad := []struct {
		name, file string
		format     domain.FeederFormat
		want       error
	}{
		{"header only", "header.csv", domain.FeederCSV, loadtest.ErrFeederEmpty},
		{"ragged", "ragged.csv", domain.FeederCSV, loadtest.ErrFeederFormat},
		{"not a list", "bad.json", domain.FeederJSON, loadtest.ErrFeederFormat},
		{"empty list", "empty.json", domain.FeederJSON, loadtest.ErrFeederEmpty},
	}

	for _, b := range bad {
		_, err := loadtest.LoadFeeders(fsys, []domain.DataFeeder{{Name: b.name, File: b.file, Format: b.format}})
		if !errors.Is(err, b.want) {
			t.Errorf("%s: got %v, want %v", b.name, err, b.want)
		}
	}

	for _, file := range []string{"missing.csv", "../secret.csv", "/etc/passwd"} {
		if _, err := loadtest.LoadFeeders(fsys, []domain.DataFeeder{{Name: "x", File: file, Format: domain.FeederCSV}}); err == nil {
			t.Errorf("%s: must not load", file)
		}
	}
}
