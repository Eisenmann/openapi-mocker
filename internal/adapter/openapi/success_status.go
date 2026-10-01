package openapi

import (
	"strconv"
)

const (
	minSuccessStatus = 200
	maxSuccessStatus = 299
)

// SuccessStatus returns the lowest 2xx status code the operation declares in
// its responses (ranges such as "2XX" and "default" are ignored).
func (e *Engine) SuccessStatus(raw []byte, method, path string) (code int, ok bool) {
	doc, err := parse(raw)
	if err != nil {
		return 0, false
	}

	m := findOperation(doc, method, path)
	if m == nil || m.Operation.Responses == nil {
		return 0, false
	}

	for status := range m.Operation.Responses.Map() {
		n, convErr := strconv.Atoi(status)
		if convErr != nil || n < minSuccessStatus || n > maxSuccessStatus {
			continue
		}

		if !ok || n < code {
			code, ok = n, true
		}
	}

	return code, ok
}
