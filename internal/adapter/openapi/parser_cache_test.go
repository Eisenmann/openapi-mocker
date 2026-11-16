package openapi

import (
	"fmt"
	"sync"
	"testing"
)

const cacheSpec = `openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a:
    get:
      responses:
        "200": {description: ok}
`

func TestParse_SameBytesShareOneDocument(t *testing.T) {
	t.Parallel()

	first, err := parse([]byte(cacheSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	second, err := parse([]byte(cacheSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if first != second {
		t.Error("expected the same document for identical contract bytes")
	}

	changed, err := parse([]byte(cacheSpec + "  /b:\n    get:\n      responses:\n        \"200\": {description: ok}\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if changed == first {
		t.Error("a changed contract must not reuse the old document")
	}
}

func TestParse_InvalidContractIsNotCached(t *testing.T) {
	t.Parallel()

	for range 2 {
		_, err := parse([]byte("openapi: [unclosed"))
		if err == nil {
			t.Fatal("expected an error for a broken contract")
		}
	}
}

func TestParseUncached_ReturnsPrivateDocument(t *testing.T) {
	t.Parallel()

	shared, err := parse([]byte(cacheSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	private, err := parseUncached([]byte(cacheSpec))
	if err != nil {
		t.Fatalf("parseUncached: %v", err)
	}

	if private == shared {
		t.Error("parseUncached must not return the shared document")
	}
}

func TestDocCache_EvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	c := newDocCache(2)

	keyOf := func(n byte) [32]byte { return [32]byte{n} }

	c.put(keyOf(1), nil)
	c.put(keyOf(2), nil)

	_, _ = c.get(keyOf(1)) // 1 is now newer than 2.

	c.put(keyOf(3), nil)

	if _, ok := c.get(keyOf(2)); ok {
		t.Error("expected the least recently used entry to be evicted")
	}

	if _, ok := c.get(keyOf(1)); !ok {
		t.Error("expected the recently used entry to stay")
	}
}

func TestParse_ConcurrentUse(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup

	for i := range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			raw := fmt.Sprintf("%s# variant %d\n", cacheSpec, i%3)

			_, err := parse([]byte(raw))
			if err != nil {
				t.Errorf("parse: %v", err)
			}
		}()
	}

	wg.Wait()
}
