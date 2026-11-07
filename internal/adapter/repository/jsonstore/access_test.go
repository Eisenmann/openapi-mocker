package jsonstore

import (
	"fmt"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func testStore(t *testing.T) *Store {
	t.Helper()

	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestAccessPersistsAcrossReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.AddToken(&domain.AccessToken{ID: "t1", ProjectID: "p", Name: "ci", Role: domain.RoleEditor, Hash: "h1", CreatedAt: now})
	s.AddShareLink(&domain.ShareLink{ID: "l1", ProjectID: "p", Label: "qa", Hash: "h2", CreatedAt: now})
	s.TouchToken("t1", now)
	s.AddAudit(&domain.AuditEntry{ProjectID: "p", Action: "x", Time: now})

	again, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	tok, ok := again.FindToken("h1")
	if !ok || tok.Name != "ci" || tok.LastUsedAt == nil || !tok.LastUsedAt.Equal(now) {
		t.Errorf("token after reload: %+v %v", tok, ok)
	}

	if l, ok := again.FindShareLink("h2"); !ok || l.Label != "qa" {
		t.Errorf("share link after reload: %+v %v", l, ok)
	}

	if got := again.ListAudit("p", &usecase.AuditQuery{}); len(got) != 1 || got[0].ID == "" {
		t.Errorf("audit after reload: %+v", got)
	}
}

func TestAccessLookupsReturnCopies(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	s.AddToken(&domain.AccessToken{ID: "t1", ProjectID: "p", Name: "a", Hash: "h"})

	tok, _ := s.FindToken("h")
	tok.Name = "changed"

	if again, _ := s.GetToken("p", "t1"); again.Name != "a" {
		t.Error("changing a copy must not change the store")
	}

	if _, err := s.GetToken("other", "t1"); err == nil {
		t.Error("a token is found only through its own project")
	}

	if err := s.UpdateToken(&domain.AccessToken{ID: "missing"}); err == nil {
		t.Error("updating an unknown token must fail")
	}

	if err := s.UpdateShareLink(&domain.ShareLink{ID: "missing"}); err == nil {
		t.Error("updating an unknown link must fail")
	}

	if _, err := s.GetShareLink("p", "missing"); err == nil {
		t.Error("unknown link")
	}

	s.TouchToken("missing", time.Now()) // must not panic.
}

func TestDeletingAProjectKeepsOnlyItsAudit(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	p := s.Create("n", "")

	s.AddToken(&domain.AccessToken{ID: "t1", ProjectID: p.ID, Hash: "h1"})
	s.AddToken(&domain.AccessToken{ID: "t2", ProjectID: "keep", Hash: "h2"})
	s.AddShareLink(&domain.ShareLink{ID: "l1", ProjectID: p.ID, Hash: "h3"})
	s.AddAudit(&domain.AuditEntry{ProjectID: p.ID, Action: "project.delete"})

	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.FindToken("h1"); ok {
		t.Error("tokens of a deleted project must go")
	}

	if _, ok := s.FindShareLink("h3"); ok {
		t.Error("share links of a deleted project must go")
	}

	if _, ok := s.FindToken("h2"); !ok {
		t.Error("other projects keep their tokens")
	}

	if len(s.ListAudit(p.ID, &usecase.AuditQuery{})) != 1 {
		t.Error("the audit log remembers the deletion")
	}
}

func TestAuditIsBoundedPerProject(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	s.auditCap = 5

	for i := range 8 {
		s.AddAudit(&domain.AuditEntry{ProjectID: "a", Action: fmt.Sprint("a", i)})
	}

	s.AddAudit(&domain.AuditEntry{ProjectID: "b", Action: "b0"})

	got := s.ListAudit("a", &usecase.AuditQuery{})
	if len(got) != 5 || got[0].Action != "a7" || got[4].Action != "a3" {
		t.Errorf("want the newest 5 of project a, got %+v", got)
	}

	if len(s.ListAudit("b", &usecase.AuditQuery{})) != 1 {
		t.Error("another project's entries are untouched")
	}
}

func TestAuditQueryFilters(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, a := range []struct{ action, actor string }{{"x", "ann"}, {"y", "bob"}, {"x", "bob"}} {
		s.AddAudit(&domain.AuditEntry{
			ProjectID: "p", Action: a.action, Actor: domain.Actor{Kind: "token", ID: "id-" + a.actor, Name: a.actor},
			Time: base.Add(time.Duration(i) * time.Hour),
		})
	}

	cases := []struct {
		q    usecase.AuditQuery
		want int
	}{
		{usecase.AuditQuery{}, 3},
		{usecase.AuditQuery{Limit: 2}, 2},
		{usecase.AuditQuery{Action: "x"}, 2},
		{usecase.AuditQuery{Actor: "bob"}, 2},
		{usecase.AuditQuery{Actor: "id-ann"}, 1},
		{usecase.AuditQuery{Since: base.Add(90 * time.Minute)}, 1},
		{usecase.AuditQuery{Action: "y", Actor: "ann"}, 0},
	}

	for _, tc := range cases {
		if got := len(s.ListAudit("p", &tc.q)); got != tc.want {
			t.Errorf("%+v: want %d, got %d", tc.q, tc.want, got)
		}
	}
}

func TestAuditTotalBound(t *testing.T) {
	t.Parallel()

	s := testStore(t)

	// Fill the in-memory log directly; saving 20000 entries one by one is slow.
	s.d.Audit = make([]domain.AuditEntry, maxAuditTotal)
	for i := range s.d.Audit {
		s.d.Audit[i] = domain.AuditEntry{ProjectID: fmt.Sprint("p", i%50)}
	}

	s.AddAudit(&domain.AuditEntry{ProjectID: "last"})

	if len(s.d.Audit) != maxAuditTotal || s.d.Audit[len(s.d.Audit)-1].ProjectID != "last" {
		t.Errorf("total bound: %d entries", len(s.d.Audit))
	}
}
