package httpapi

import (
	"testing"
	"time"
)

func TestMCPSessionsLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newMCPSessions()
	s.now = func() time.Time { return now }

	id := s.start("p1")
	if len(id) != sessionIDBytes*2 {
		t.Fatalf("session id %q", id)
	}

	if _, ok := s.get("p2", id); ok {
		t.Error("a session belongs to its project")
	}

	sess, ok := s.get("p1", id)
	if !ok {
		t.Fatal("session must exist")
	}

	now = now.Add(mcpSessionTTL - time.Minute)

	if _, ok := s.get("p1", id); !ok {
		t.Error("use refreshes the session")
	}

	now = now.Add(mcpSessionTTL + time.Minute)

	if _, ok := s.get("p1", id); ok {
		t.Error("an idle session expires")
	}

	select {
	case <-sess.done:
	default:
		t.Error("expiry closes the session's streams")
	}
}

func TestMCPSessionsEndAndEviction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newMCPSessions()
	s.now = func() time.Time { return now }

	first := s.start("p")

	if s.end("other", first) || !s.end("p", first) || s.end("p", first) {
		t.Error("end succeeds once, for the owning project only")
	}

	oldest := s.start("p")

	for i := 1; i < maxMCPSessions; i++ {
		now = now.Add(time.Second)
		s.start("p")
	}

	now = now.Add(time.Second)
	s.start("p")

	if _, ok := s.get("p", oldest); ok {
		t.Error("the oldest session makes room when the store is full")
	}

	if len(s.byID) != maxMCPSessions {
		t.Errorf("store size %d", len(s.byID))
	}
}

func TestMCPSessionsDropsExpiredOnStart(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newMCPSessions()
	s.now = func() time.Time { return now }

	s.start("p")

	now = now.Add(2 * mcpSessionTTL)
	s.start("p")

	if len(s.byID) != 1 {
		t.Errorf("expired sessions must be dropped, have %d", len(s.byID))
	}
}

func TestSplitBatchAndRPCHelpers(t *testing.T) {
	t.Parallel()

	if got := splitBatch([]byte(`[{"a":1},{"b":2}]`)); len(got) != 2 || string(got[1]) != `{"b":2}` {
		t.Errorf("batch: %q", got)
	}

	if got := splitBatch([]byte(`[bad`)); len(got) != 1 {
		t.Errorf("invalid batch stays whole: %q", got)
	}

	if got := splitBatch([]byte(`{"a":1}`)); len(got) != 1 {
		t.Errorf("single: %q", got)
	}

	if rpcMethod([]byte(`{"method":"initialize"}`)) != "initialize" || rpcMethod([]byte(`[`)) != "" {
		t.Error("rpcMethod")
	}

	if !hasResult([]byte(`{"result":{}}`)) || hasResult([]byte(`{"error":{}}`)) {
		t.Error("hasResult")
	}

	if progressToken([]byte(`{"method":"tools/list","params":{"_meta":{"progressToken":1}}}`)) != nil {
		t.Error("progress only for tools/call")
	}
}
