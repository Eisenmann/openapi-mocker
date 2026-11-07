// Package exchange reads and writes the file formats other tools use for API
// requests: Postman collections, Insomnia exports and HAR files, plus the
// detection of what an uploaded file is. It implements usecase.ExchangeCodec.
package exchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/invopop/yaml"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var (
	// ErrUnknownFile is returned for a file that is none of the known formats.
	ErrUnknownFile = errors.New("not a recognized Postman, Insomnia, HAR, OpenAPI/Swagger or openapi-mocker file")
	// ErrUnknownExport is returned for an export format that is not offered.
	ErrUnknownExport = errors.New("unknown collection format")
)

// Codec is the usecase.ExchangeCodec of the application.
type Codec struct{}

// New returns the codec.
func New() *Codec { return &Codec{} }

var _ usecase.ExchangeCodec = (*Codec)(nil)

// DetectImport says what kind of file data is.
func (c *Codec) DetectImport(data []byte) (string, error) {
	doc, ok := decodeLoose(data)
	if !ok {
		return "", ErrUnknownFile
	}

	switch {
	case doc["format"] == usecase.BundleFormat:
		return usecase.KindBundle, nil
	case isHAR(doc):
		return usecase.KindHAR, nil
	case isInsomnia(doc):
		return usecase.KindInsomnia, nil
	case isPostman(doc):
		return usecase.KindPostman, nil
	case doc["openapi"] != nil || doc["swagger"] != nil:
		return usecase.KindOpenAPI, nil
	default:
		return "", ErrUnknownFile
	}
}

func isHAR(doc map[string]any) bool {
	log, ok := doc["log"].(map[string]any)
	if !ok {
		return false
	}

	_, has := log["entries"]

	return has
}

func isInsomnia(doc map[string]any) bool {
	return doc[keyType] == "export" || strings.HasPrefix(str(doc["type"]), "collection.insomnia.rest")
}

func isPostman(doc map[string]any) bool {
	info, ok := doc["info"].(map[string]any)
	if !ok {
		return false
	}

	_, hasItems := doc["item"]

	return hasItems && (strings.Contains(str(info[keySchema]), "getpostman") || info["_postman_id"] != nil)
}

// ParseImport reads a Postman, Insomnia or HAR file.
func (c *Codec) ParseImport(kind string, data []byte) (*usecase.ImportedAPI, error) {
	switch kind {
	case usecase.KindPostman:
		return parsePostman(data)
	case usecase.KindInsomnia:
		return parseInsomnia(data)
	case usecase.KindHAR:
		return parseHAR(data)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFile, kind)
	}
}

// ExportCollection writes the project as a Postman or Insomnia collection.
func (c *Codec) ExportCollection(format string, data *usecase.ExportData) ([]byte, error) {
	switch format {
	case usecase.CollectionPostman:
		return exportPostman(data)
	case usecase.CollectionInsomnia:
		return exportInsomnia(data)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownExport, format)
	}
}

// decodeLoose reads a JSON or YAML document as a generic object.
func decodeLoose(data []byte) (map[string]any, bool) {
	data = bytes.TrimPrefix(bytes.TrimSpace(data), []byte("\xef\xbb\xbf"))
	if len(data) == 0 {
		return nil, false
	}

	if data[0] != '{' {
		converted, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, false
		}

		data = converted
	}

	var doc map[string]any

	err := json.Unmarshal(data, &doc)
	if err != nil || doc == nil {
		return nil, false
	}

	return doc, true
}

// toJSON returns data as JSON, converting YAML when needed.
func toJSON(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(bytes.TrimSpace(data), []byte("\xef\xbb\xbf"))
	if len(data) > 0 && data[0] == '{' {
		return data, nil
	}

	out, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	return out, nil
}

func str(v any) string {
	s, _ := v.(string)

	return s
}
