// Package openapi is the interface adapter (Interface Adapter in Clean
// Architecture terms) encapsulating a specific OpenAPI parsing library
// (kin-openapi).
// It exposes only Engine, implemented with usecase-layer primitive types
// (usecase.Endpoint, usecase.DiffLine, etc.) — no kin-openapi type ever
// leaves this package.
package openapi

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

// parse returns the parsed contract. Parsing is the most expensive step of
// serving a mock request (a YAML decode and a conversion of the whole
// document), and every request needs it several times, so the result is kept
// by content: the same bytes give the same document. The document is shared
// between callers and goroutines and must be treated as read-only.
func parse(raw []byte) (*openapi3.T, error) {
	return parseCached(parsedDocs, raw)
}

// parseCached is parse over a given cache.
func parseCached(cache *docCache, raw []byte) (*openapi3.T, error) {
	key := sha256.Sum256(raw)

	doc, ok := cache.get(key)
	if ok {
		return doc, nil
	}

	doc, err := parseUncached(raw)
	if err != nil {
		return nil, err
	}

	cache.put(key, doc)

	return doc, nil
}

// parseUncached always builds a new document that the caller owns. Use it
// where the document is modified; parse hands out a shared one.
func parseUncached(raw []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false

	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract: %w", err)
	}

	return doc, nil
}

// maxParsedDocs is how many different contracts are kept parsed: enough for
// the active contracts of a few projects, small enough to bound the memory.
const maxParsedDocs = 8

var parsedDocs = newDocCache(maxParsedDocs)

// docCache is a small least-recently-used cache of parsed documents.
type docCache struct {
	mu  sync.Mutex
	max int
	// order lists the entries, the most recently used first.
	order   *list.List
	entries map[[sha256.Size]byte]*list.Element
}

type docEntry struct {
	key [sha256.Size]byte
	doc *openapi3.T
}

func newDocCache(maxEntries int) *docCache {
	return &docCache{
		mu:      sync.Mutex{},
		max:     maxEntries,
		order:   list.New(),
		entries: make(map[[sha256.Size]byte]*list.Element, maxEntries),
	}
}

func (c *docCache) get(key [sha256.Size]byte) (*openapi3.T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}

	c.order.MoveToFront(el)

	entry, _ := el.Value.(*docEntry)

	return entry.doc, true
}

func (c *docCache) put(key [sha256.Size]byte, doc *openapi3.T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return
	}

	c.entries[key] = c.order.PushFront(&docEntry{key: key, doc: doc})

	for c.order.Len() > c.max {
		oldest := c.order.Back()
		entry, _ := oldest.Value.(*docEntry)

		delete(c.entries, entry.key)
		c.order.Remove(oldest)
	}
}

func parseAndValidate(raw []byte) (*openapi3.T, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}

	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("contract failed validation: %w", err)
	}

	return doc, nil
}
