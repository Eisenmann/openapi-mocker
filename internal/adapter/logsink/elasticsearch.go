package logsink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var _ usecase.LogSink = (*Elasticsearch)(nil)

// DefaultIndex is the index (or data stream) entries are written to.
const DefaultIndex = "openapi-mocker-logs"

// dateToken in the index name is replaced by the entry's UTC date (2006.01.02),
// for daily indices such as "openapi-mocker-{date}".
const dateToken = "{date}"

// ESConfig configures the Elasticsearch/OpenSearch sink.
type ESConfig struct {
	// URL is the cluster endpoint, e.g. https://es.example.com:9200.
	URL string
	// Index is the index or data stream; it may contain "{date}".
	Index string
	// APIKey (Elasticsearch) takes precedence over Username and Password.
	APIKey   string
	Username string
	Password string
	Client   *http.Client
}

// Elasticsearch writes entries with the _bulk API. It works with
// Elasticsearch and OpenSearch.
type Elasticsearch struct {
	cfg ESConfig
}

// NewElasticsearch builds the sink.
func NewElasticsearch(cfg *ESConfig) (*Elasticsearch, error) {
	if cfg.URL == "" {
		return nil, ErrNoURL
	}

	c := *cfg
	c.URL = strings.TrimRight(c.URL, "/")

	if c.Index == "" {
		c.Index = DefaultIndex
	}

	if c.Client == nil {
		c.Client = http.DefaultClient
	}

	return &Elasticsearch{cfg: c}, nil
}

// Name implements usecase.LogSink.
func (*Elasticsearch) Name() string { return "elasticsearch" }

// Send implements usecase.LogSink.
func (e *Elasticsearch) Send(ctx context.Context, batch []*domain.RequestLog) error {
	var body bytes.Buffer

	enc := newEncoder(&body)

	for _, l := range batch {
		index := strings.ReplaceAll(e.cfg.Index, dateToken, l.Timestamp.UTC().Format("2006.01.02"))

		// "create" works for both plain indices and data streams, and the
		// entry id makes a retried batch idempotent (a duplicate is a 409).
		err := enc.Encode(map[string]any{"create": map[string]string{"_index": index, "_id": l.ID}})
		if err != nil {
			return err
		}

		err = enc.Encode(ToRecord(l))
		if err != nil {
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.URL+"/_bulk", &body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-ndjson")

	switch {
	case e.cfg.APIKey != "":
		req.Header.Set("Authorization", "ApiKey "+e.cfg.APIKey)
	case e.cfg.Username != "":
		req.SetBasicAuth(e.cfg.Username, e.cfg.Password)
	}

	return e.do(req)
}

var errBulk = errors.New("elasticsearch rejected entries")

func (e *Elasticsearch) do(req *http.Request) error {
	resp, err := e.cfg.Client.Do(req) //nolint:gosec // the URL is operator configuration.
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bulkReplyBytes))

	if resp.StatusCode/statusClassDiv != successClass {
		return fmt.Errorf("%w: %s: %s", errStatus, resp.Status, bytes.TrimSpace(truncate(raw, errorBodyBytes)))
	}

	return checkBulk(raw)
}

// checkBulk fails when any item was rejected, other than as a duplicate.
func checkBulk(raw []byte) error {
	var out struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}

	err := json.Unmarshal(raw, &out)
	if err != nil || !out.Errors {
		return nil //nolint:nilerr // an unreadable 2xx body is not worth a retry.
	}

	failed := 0
	first := ""

	for _, item := range out.Items {
		for _, r := range item {
			if r.Status == http.StatusConflict || r.Status/statusClassDiv == successClass {
				continue
			}

			failed++

			if first == "" {
				first = r.Error.Type + ": " + r.Error.Reason
			}
		}
	}

	if failed == 0 {
		return nil
	}

	return fmt.Errorf("%w: %d of %d (first: %s)", errBulk, failed, len(out.Items), first)
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}

	return b
}
