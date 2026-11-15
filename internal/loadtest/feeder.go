package loadtest

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"sync/atomic"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Errors of data feeders.
var (
	ErrFeederEmpty     = errors.New("data feeder has no rows")
	ErrFeederFormat    = errors.New("data feeder file is not valid")
	ErrFeederExhausted = errors.New("data feeder has run out of rows")
)

// maxFeederBytes bounds a feeder file so a wrong path cannot fill memory.
const maxFeederBytes = 64 << 20

// Feeder hands out rows of test data to iterations. It is safe for concurrent
// use.
type Feeder struct {
	name string
	mode domain.FeederMode
	rows []map[string]any
	next atomic.Uint64
	// exhausted is set the first time a unique feeder has no row left.
	exhausted atomic.Bool
}

// NewFeeder returns a feeder over rows. An empty mode means sequential.
func NewFeeder(name string, mode domain.FeederMode, rows []map[string]any) (*Feeder, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrFeederEmpty, name)
	}

	f := new(Feeder)
	f.name = name
	f.mode = mode
	f.rows = rows

	if f.mode == "" {
		f.mode = domain.FeederSequential
	}

	return f, nil
}

// Name is the feeder's name in {{data.<name>.<column>}}.
func (f *Feeder) Name() string { return f.name }

// Rows is the number of rows.
func (f *Feeder) Rows() int { return len(f.rows) }

// Exhausted reports whether a unique feeder ran out of rows.
func (f *Feeder) Exhausted() bool { return f.exhausted.Load() }

// Next returns the next row: in order and wrapping around (sequential), a
// random one (random), or each row once (unique). ok is false when a unique
// feeder has no row left; the scenario should stop then.
func (f *Feeder) Next() (row map[string]any, ok bool) {
	switch f.mode {
	case domain.FeederRandom:
		return f.rows[rand.IntN(len(f.rows))], true //nolint:gosec // test data choice, not security
	case domain.FeederUnique:
		n := f.next.Add(1) - 1
		if n >= uint64(len(f.rows)) {
			f.exhausted.Store(true)

			return nil, false
		}

		return f.rows[n], true
	case domain.FeederSequential:
	}

	return f.cycle(), true
}

// cycle returns the next row, wrapping around at the end.
func (f *Feeder) cycle() map[string]any {
	n := f.next.Add(1) - 1

	return f.rows[n%uint64(len(f.rows))]
}

// LoadFeeders reads the plan's data feeders from files in fsys (the plan's
// directory). Paths cannot leave fsys.
func LoadFeeders(fsys fs.FS, feeders []domain.DataFeeder) (map[string]*Feeder, error) {
	out := make(map[string]*Feeder, len(feeders))

	for i := range feeders {
		fd := &feeders[i]

		data, err := readLimited(fsys, fd.File)
		if err != nil {
			return nil, fmt.Errorf("data %q: %w", fd.Name, err)
		}

		rows, err := parseRows(fd.Format, data)
		if err != nil {
			return nil, fmt.Errorf("data %q (%s): %w", fd.Name, fd.File, err)
		}

		f, err := NewFeeder(fd.Name, fd.Mode, rows)
		if err != nil {
			return nil, err
		}

		out[fd.Name] = f
	}

	return out, nil
}

func readLimited(fsys fs.FS, name string) ([]byte, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxFeederBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(data) > maxFeederBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrFeederFormat, maxFeederBytes)
	}

	return data, nil
}

func parseRows(format domain.FeederFormat, data []byte) ([]map[string]any, error) {
	if format == domain.FeederJSON {
		return parseJSONRows(data)
	}

	return parseCSVRows(data)
}

// parseCSVRows reads a CSV whose first line holds the column names.
func parseCSVRows(data []byte) ([]map[string]any, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFeederFormat, err)
	}

	if len(records) < 2 { //nolint:mnd // a header plus at least one row
		return nil, ErrFeederEmpty
	}

	header := records[0]
	rows := make([]map[string]any, 0, len(records)-1)

	for _, rec := range records[1:] {
		row := make(map[string]any, len(header))
		for i, col := range header {
			row[col] = rec[i] // ReadAll guarantees len(header) fields per record.
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// parseJSONRows reads an array of objects; an array of plain values becomes
// rows with the single column "value".
func parseJSONRows(data []byte) ([]map[string]any, error) {
	var items []any

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	err := dec.Decode(&items)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFeederFormat, err)
	}

	rows := make([]map[string]any, 0, len(items))

	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			rows = append(rows, m)

			continue
		}

		rows = append(rows, map[string]any{"value": it})
	}

	return rows, nil
}
