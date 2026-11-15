package report

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// JSON renders the report as indented JSON.
func JSON(r *Report) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	err := enc.Encode(r)
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}

	return buf.Bytes(), nil
}

// Parse reads a report written by JSON, so other formats can be produced
// from a stored run.
func Parse(data []byte) (*Report, error) {
	var r Report

	err := json.Unmarshal(data, &r)
	if err != nil {
		return nil, fmt.Errorf("decode report: %w", err)
	}

	return &r, nil
}
