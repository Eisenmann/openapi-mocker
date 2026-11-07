package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/logsink"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/memlog"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Logging modes (LOG_MODE).
const (
	logModeLocal       = "local"
	logModeDistributed = "distributed"

	sinkStdout        = "stdout"
	sinkHTTP          = "http"
	sinkElasticsearch = "elasticsearch"

	sinkHTTPTimeout = 10 * time.Second
)

var errLogConfig = errors.New("invalid logging configuration")

// logConfig is the logging configuration read from the environment:
//
//	LOG_MODE           local (default): requests are logged to the data file.
//	                   distributed: requests are shipped to external systems
//	                   and only a recent window is kept in memory (for the UI).
//	LOG_SINKS          comma-separated: stdout, http, elasticsearch. The default
//	                   is "stdout" in distributed mode and none in local mode.
//	LOG_EXPORT_BODIES  true to ship request and response bodies too.
//	LOG_HTTP_URL, LOG_HTTP_TOKEN
//	                   the "http" sink: NDJSON POSTs (Logstash, Vector).
//	LOG_ES_URL, LOG_ES_INDEX, LOG_ES_API_KEY, LOG_ES_USER, LOG_ES_PASSWORD
//	                   the "elasticsearch" sink (Elasticsearch and OpenSearch);
//	                   the index may contain {date} for daily indices.
type logConfig struct {
	mode         string
	sinks        []string
	exportBodies bool

	httpURL, httpToken string

	esURL, esIndex, esAPIKey, esUser, esPassword string
}

// logConfigFrom reads the configuration through getenv.
func logConfigFrom(getenv func(string) string) (logConfig, error) {
	cfg := logConfig{
		mode:         strings.ToLower(strings.TrimSpace(getenv("LOG_MODE"))),
		exportBodies: isTrue(getenv("LOG_EXPORT_BODIES")),
		httpURL:      getenv("LOG_HTTP_URL"), httpToken: getenv("LOG_HTTP_TOKEN"),
		esURL: getenv("LOG_ES_URL"), esIndex: getenv("LOG_ES_INDEX"), esAPIKey: getenv("LOG_ES_API_KEY"),
		esUser: getenv("LOG_ES_USER"), esPassword: getenv("LOG_ES_PASSWORD"),
	}

	switch cfg.mode {
	case "":
		cfg.mode = logModeLocal
	case logModeLocal, logModeDistributed:
	default:
		return cfg, fmt.Errorf("%w: LOG_MODE must be %q or %q, got %q",
			errLogConfig, logModeLocal, logModeDistributed, cfg.mode)
	}

	for _, s := range strings.Split(getenv("LOG_SINKS"), ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}

		switch s {
		case sinkStdout, sinkHTTP, sinkElasticsearch:
			cfg.sinks = append(cfg.sinks, s)
		default:
			return cfg, fmt.Errorf("%w: unknown LOG_SINKS entry %q", errLogConfig, s)
		}
	}

	if len(cfg.sinks) == 0 && cfg.mode == logModeDistributed {
		cfg.sinks = []string{sinkStdout}
	}

	return cfg, nil
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// logPipeline is the log store, hub options and exporter chosen by the config.
type logPipeline struct {
	repo     usecase.LogRepository
	options  []usecase.LogHubOption
	exporter *usecase.LogExporter
}

// buildLogPipeline picks the store (the data file, or memory in distributed
// mode) and starts the exporter for the configured sinks.
func buildLogPipeline(cfg *logConfig, local usecase.LogRepository, stdout io.Writer) (*logPipeline, error) {
	p := &logPipeline{repo: local, options: []usecase.LogHubOption{usecase.WithLogMode(cfg.mode)}, exporter: nil}

	if cfg.mode == logModeDistributed {
		p.repo = memlog.New(memlog.DefaultCapacity)
	}

	sinks, err := buildSinks(cfg, stdout)
	if err != nil {
		return nil, err
	}

	if len(sinks) > 0 {
		p.exporter = usecase.NewLogExporter(sinks)
		p.options = append(p.options, usecase.WithLogExport(p.exporter, cfg.exportBodies))
	}

	return p, nil
}

func buildSinks(cfg *logConfig, stdout io.Writer) ([]usecase.LogSink, error) {
	client := &http.Client{Timeout: sinkHTTPTimeout}
	sinks := make([]usecase.LogSink, 0, len(cfg.sinks))

	for _, name := range cfg.sinks {
		switch name {
		case sinkStdout:
			sinks = append(sinks, logsink.NewStdout(stdout))
		case sinkHTTP:
			s, err := logsink.NewHTTP(cfg.httpURL, cfg.httpToken, client)
			if err != nil {
				return nil, fmt.Errorf("%w: sink %q needs LOG_HTTP_URL", errLogConfig, name)
			}

			sinks = append(sinks, s)
		case sinkElasticsearch:
			s, err := logsink.NewElasticsearch(&logsink.ESConfig{
				URL: cfg.esURL, Index: cfg.esIndex, APIKey: cfg.esAPIKey,
				Username: cfg.esUser, Password: cfg.esPassword, Client: client,
			})
			if err != nil {
				return nil, fmt.Errorf("%w: sink %q needs LOG_ES_URL", errLogConfig, name)
			}

			sinks = append(sinks, s)
		}
	}

	return sinks, nil
}

// newLogHub reads the logging configuration and builds the hub that every
// serving service logs through, plus the exporter to flush at shutdown.
func newLogHub(local usecase.LogRepository, stdout io.Writer) (*usecase.LogHub, *usecase.LogExporter, error) {
	cfg, err := logConfigFrom(os.Getenv)
	if err != nil {
		return nil, nil, err
	}

	p, err := buildLogPipeline(&cfg, local, stdout)
	if err != nil {
		return nil, nil, err
	}

	return usecase.NewLogHub(p.repo, capturePolicyFromEnv(), p.options...), p.exporter, nil
}
