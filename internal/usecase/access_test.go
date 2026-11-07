package usecase_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type accessFixture struct {
	svc   *usecase.AccessService
	store *jsonstore.Store
	pid   string
	now   *time.Time
}

func newAccessFixture(t *testing.T, opts ...usecase.AccessOption) *accessFixture {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	f := &accessFixture{store: store, now: &now, pid: store.Create("p", "").ID}
	opts = append([]usecase.AccessOption{usecase.WithAccessClock(func() time.Time { return *f.now })}, opts...)
	f.svc = usecase.NewAccessService(store, store, store, opts...)

	return f
}

func (f *accessFixture) token(t *testing.T, role domain.Role, ttl int) usecase.CreatedToken {
	t.Helper()

	c, err := f.svc.CreateToken(f.pid, usecase.TokenInput{Name: string(role), Role: role, TTLDays: ttl}, domain.Anonymous())
	if err != nil {
		t.Fatalf("create %s token: %v", role, err)
	}

	return c
}

func TestAccessService_OpenProjectIsAnonymousAdmin(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)

	p, err := f.svc.Authenticate(f.pid, usecase.Credentials{})
	if err != nil || p.Actor.Kind != domain.ActorAnonymous || p.Role != domain.RoleAdmin || f.svc.Locked(f.pid) {
		t.Errorf("open project: %v %v", p, err)
	}

	if !strings.Contains(p.String(), "anonymous") {
		t.Errorf("principal text %q", p.String())
	}
}

func TestAccessService_TokenLifecycle(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)
	admin := f.token(t, domain.RoleAdmin, 0)
	editor := f.token(t, domain.RoleEditor, 1)

	p, err := f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: editor.Secret})
	if err != nil || p.Role != domain.RoleEditor || p.Actor.Name != "editor" || p.Actor.Kind != domain.ActorToken {
		t.Fatalf("editor: %v %v", p, err)
	}

	// Use is recorded, but at most once a minute.
	first := f.svc.ListTokens(f.pid)
	if first[0].LastUsedAt == nil || !first[0].LastUsedAt.Equal(*f.now) {
		t.Fatalf("last use not recorded: %+v", first[0])
	}

	*f.now = f.now.Add(10 * time.Second)

	_, _ = f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: editor.Secret})

	if got := f.svc.ListTokens(f.pid)[0].LastUsedAt; !got.Equal(*first[0].LastUsedAt) {
		t.Error("last use is refreshed at most once a minute")
	}

	*f.now = f.now.Add(2 * time.Minute)

	_, _ = f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: editor.Secret})

	if got := f.svc.ListTokens(f.pid)[0].LastUsedAt; !got.After(*first[0].LastUsedAt) {
		t.Error("last use must move forward after a minute")
	}

	// Expiry: after a day the editor token no longer works, yet the project stays locked.
	*f.now = f.now.Add(25 * time.Hour)

	if _, err := f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: editor.Secret}); !errors.Is(err, usecase.ErrUnauthenticated) {
		t.Errorf("expired token: %v", err)
	}

	if !f.svc.ListTokens(f.pid)[0].Expired {
		t.Error("the list must flag an expired token")
	}

	if _, err := f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: admin.Secret}); err != nil {
		t.Errorf("a token without expiry keeps working: %v", err)
	}

	for _, cred := range []usecase.Credentials{{}, {Bearer: "omk_nope"}, {Share: "oms_nope"}} {
		if _, err := f.svc.Authenticate(f.pid, cred); !errors.Is(err, usecase.ErrUnauthenticated) {
			t.Errorf("%+v: %v", cred, err)
		}
	}
}

func TestAccessService_AdminTokenWorksEverywhere(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t, usecase.WithAdminToken("root-secret"))
	f.token(t, domain.RoleAdmin, 0)

	p, err := f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: "root-secret"})
	if err != nil || p.Actor.Kind != domain.ActorAdmin || p.Role != domain.RoleAdmin {
		t.Errorf("admin token: %v %v", p, err)
	}

	if _, err := f.svc.Authenticate(f.pid, usecase.Credentials{Bearer: "root-secreT"}); err == nil {
		t.Error("a near miss must fail")
	}

	// Without a configured admin token, an empty bearer must not match it.
	g := newAccessFixture(t)
	g.token(t, domain.RoleAdmin, 0)

	if _, err := g.svc.Authenticate(g.pid, usecase.Credentials{}); err == nil {
		t.Error("an empty credential must not pass")
	}
}

func TestAccessService_CreateTokenRules(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)

	bad := []usecase.TokenInput{
		{Name: "", Role: domain.RoleAdmin},
		{Name: strings.Repeat("x", domain.MaxLabelLen+1), Role: domain.RoleAdmin},
		{Name: "n", Role: "root"},
		{Name: "n", Role: domain.RoleAdmin, TTLDays: -1},
		{Name: "n", Role: domain.RoleAdmin, TTLDays: domain.MaxTTLDays + 1},
		{Name: "n", Role: domain.RoleViewer}, // the first token must be an admin token.
	}

	for _, in := range bad {
		if _, err := f.svc.CreateToken(f.pid, in, domain.Anonymous()); !errors.Is(err, domain.ErrInvalidAccess) {
			t.Errorf("%+v: %v", in, err)
		}
	}

	if _, err := f.svc.CreateToken("missing", usecase.TokenInput{Name: "n", Role: domain.RoleAdmin}, domain.Anonymous()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}

	f.token(t, domain.RoleAdmin, 0)

	for i := 1; i < domain.MaxTokensPerProject; i++ {
		if _, err := f.svc.CreateToken(f.pid, usecase.TokenInput{Name: fmt.Sprint("t", i), Role: domain.RoleViewer}, domain.Anonymous()); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}

	if _, err := f.svc.CreateToken(f.pid, usecase.TokenInput{Name: "one too many", Role: domain.RoleViewer}, domain.Anonymous()); !errors.Is(err, domain.ErrInvalidAccess) {
		t.Errorf("token limit: %v", err)
	}
}

func TestAccessService_RevokeKeepsAnAdmin(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)
	admin := f.token(t, domain.RoleAdmin, 0)
	admin2 := f.token(t, domain.RoleAdmin, 0)
	viewer := f.token(t, domain.RoleViewer, 0)

	// With a second admin, the first may go.
	if err := f.svc.RevokeToken(f.pid, admin.Info.ID); err != nil {
		t.Fatalf("revoke first admin: %v", err)
	}

	if err := f.svc.RevokeToken(f.pid, admin2.Info.ID); !errors.Is(err, domain.ErrInvalidAccess) {
		t.Errorf("the last admin must stay while a viewer token exists: %v", err)
	}

	if err := f.svc.RevokeToken(f.pid, viewer.Info.ID); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.RevokeToken(f.pid, admin2.Info.ID); err != nil {
		t.Errorf("the only live token may be revoked: %v", err)
	}

	if f.svc.Locked(f.pid) {
		t.Error("no live token: the project is open")
	}

	if err := f.svc.RevokeToken(f.pid, "zzz"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown token: %v", err)
	}
}

func TestAccessService_ShareLinks(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)
	f.token(t, domain.RoleAdmin, 0)

	link, err := f.svc.CreateShareLink(f.pid, usecase.LinkInput{Label: "qa", TTLDays: 2}, domain.Anonymous())
	if err != nil || !strings.HasPrefix(link.Secret, "oms_") {
		t.Fatalf("create link: %v %+v", err, link)
	}

	p, err := f.svc.Authenticate(f.pid, usecase.Credentials{Share: link.Secret})
	if err != nil || !p.Share || p.Role != domain.RoleViewer || p.Actor.Kind != domain.ActorShareLink || p.Actor.Name != "qa" {
		t.Errorf("share principal: %v %v", p, err)
	}

	if got, err := f.svc.ResolveShare(link.Secret); err != nil || got.ProjectID != f.pid {
		t.Errorf("resolve: %v %v", got, err)
	}

	*f.now = f.now.Add(49 * time.Hour)

	if _, err := f.svc.ResolveShare(link.Secret); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired link: %v", err)
	}

	if !f.svc.ListShareLinks(f.pid)[0].Expired {
		t.Error("the list must flag an expired link")
	}

	if _, err := f.svc.Authenticate(f.pid, usecase.Credentials{Share: link.Secret}); !errors.Is(err, usecase.ErrUnauthenticated) {
		t.Errorf("expired link on a route: %v", err)
	}

	for _, in := range []usecase.LinkInput{{Label: ""}, {Label: "x", TTLDays: -1}} {
		if _, err := f.svc.CreateShareLink(f.pid, in, domain.Anonymous()); !errors.Is(err, domain.ErrInvalidAccess) {
			t.Errorf("%+v: %v", in, err)
		}
	}

	if _, err := f.svc.CreateShareLink("missing", usecase.LinkInput{Label: "x"}, domain.Anonymous()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}

	for i := 1; i < domain.MaxLinksPerProject; i++ {
		if _, err := f.svc.CreateShareLink(f.pid, usecase.LinkInput{Label: fmt.Sprint("l", i)}, domain.Anonymous()); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}

	if _, err := f.svc.CreateShareLink(f.pid, usecase.LinkInput{Label: "too many"}, domain.Anonymous()); !errors.Is(err, domain.ErrInvalidAccess) {
		t.Errorf("link limit: %v", err)
	}

	if err := f.svc.RevokeShareLink(f.pid, "zzz"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown link: %v", err)
	}
}

func TestAccessService_ProjectOfMockAndAudit(t *testing.T) {
	t.Parallel()

	f := newAccessFixture(t)
	m := f.store.CreateMock(&domain.MockRule{ProjectID: f.pid, Method: "GET", Path: "/x"})

	if id, ok := f.svc.ProjectOfMock(m.ID); !ok || id != f.pid {
		t.Errorf("mock project: %q %v", id, ok)
	}

	if _, ok := f.svc.ProjectOfMock("zzz"); ok {
		t.Error("unknown mock")
	}

	if _, ok := usecase.NewAccessService(f.store, f.store, nil).ProjectOfMock(m.ID); ok {
		t.Error("without a mock source nothing is found")
	}

	f.svc.Record(&domain.AuditEntry{ProjectID: f.pid, Action: "a"})

	got := f.svc.ListAudit(f.pid, &usecase.AuditQuery{})
	if len(got) != 1 || !got[0].Time.Equal(*f.now) {
		t.Errorf("record fills in the time: %+v", got)
	}
}

func TestAccessErrorsMapToStatus(t *testing.T) {
	t.Parallel()

	for err, want := range map[error]int{
		usecase.ErrUnauthenticated: usecase.StatusUnauthorized,
		usecase.ErrForbidden:       usecase.StatusForbidden,
		domain.ErrInvalidAccess:    usecase.StatusBadRequest,
	} {
		if got := usecase.StatusFromError(fmt.Errorf("wrapped: %w", err)); got != want {
			t.Errorf("%v: want %d, got %d", err, want, got)
		}
	}
}

func TestRoleOrdering(t *testing.T) {
	t.Parallel()

	if !domain.RoleAdmin.Allows(domain.RoleViewer) || domain.RoleViewer.Allows(domain.RoleEditor) ||
		!domain.RoleEditor.Allows(domain.RoleEditor) || domain.Role("").Allows(domain.RoleViewer) ||
		domain.RoleAdmin.Allows("bogus") {
		t.Error("role ordering is viewer < editor < admin, and unknown roles allow nothing")
	}
}

func TestMockUpdateKeepsItsProject(t *testing.T) {
	t.Parallel()

	repo := &projectKeepingMocks{stored: &domain.MockRule{ID: "m1", ProjectID: "owner"}}
	svc := usecase.NewMockService(repo, newMockContractRepo(), nil, nil, nil)

	err := svc.Update(&domain.MockRule{ID: "m1", ProjectID: "attacker", Method: "GET", Path: "/x", StatusCode: 200})
	if err != nil || repo.updated.ProjectID != "owner" {
		t.Errorf("the stored project must win: %v %+v", err, repo.updated)
	}

	if err := svc.Update(&domain.MockRule{ID: "gone", Method: "GET", Path: "/x", StatusCode: 200}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating an unknown mock: %v", err)
	}
}

type projectKeepingMocks struct {
	stored  *domain.MockRule
	updated *domain.MockRule
}

func (r *projectKeepingMocks) CreateMock(m *domain.MockRule) *domain.MockRule { return m }
func (r *projectKeepingMocks) UpdateMock(m *domain.MockRule) error {
	r.updated = m

	return nil
}
func (r *projectKeepingMocks) DeleteMock(string) error             { return nil }
func (r *projectKeepingMocks) ListMocks(string) []*domain.MockRule { return nil }
func (r *projectKeepingMocks) GetMock(id string) (*domain.MockRule, error) {
	if id != r.stored.ID {
		return nil, domain.ErrNotFound
	}

	return r.stored, nil
}
