package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const healthUsage = `Usage: openapi-mocker healthcheck [--url URL] [--timeout DURATION]

Asks a running server whether it is healthy; for Docker HEALTHCHECK and CI
scripts, so an image needs no curl or wget. The default URL is
http://127.0.0.1:$PORT/healthz (PORT defaults to 8080); the default timeout
is 3s.

Exit status: 0 healthy, 1 not healthy or not reachable, 2 usage error.
`

const defaultHealthTimeout = 3 * time.Second

type healthOptions struct {
	url     string
	timeout time.Duration
}

func parseHealthArgs(args []string) (healthOptions, error) {
	opts := healthOptions{url: "http://127.0.0.1:" + getEnv("PORT", "8080") + "/healthz", timeout: defaultHealthTimeout}

	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return opts, fmt.Errorf("%w: %s needs a value", errServeUsage, args[i])
		}

		switch args[i] {
		case "--url":
			opts.url = args[i+1]
		case "--timeout":
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return opts, fmt.Errorf("%w: --timeout must be a duration such as 3s", errServeUsage)
			}

			opts.timeout = d
		default:
			return opts, fmt.Errorf("%w: unknown flag %s", errServeUsage, args[i])
		}

		i++
	}

	return opts, nil
}

// runHealthcheck is the healthcheck command; it returns the process exit code.
func runHealthcheck(args []string, stderr io.Writer) int {
	opts, err := parseHealthArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n\n%s", err, healthUsage)
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.url, http.NoBody)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return exitUsage
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return exitIOFailure
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %s answered %s\n", opts.url, resp.Status)
		return exitIOFailure
	}

	return exitOK
}
