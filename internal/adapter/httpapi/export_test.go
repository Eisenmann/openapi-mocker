package httpapi

import "time"

// SetMCPKeepAlive shortens the GET stream keep-alive for a test and returns
// the function that restores it.
func SetMCPKeepAlive(d time.Duration) (restore func()) {
	old := mcpKeepAlive
	mcpKeepAlive = d

	return func() { mcpKeepAlive = old }
}
