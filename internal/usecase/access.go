package usecase

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/idgen"
)

// Errors of the access control.
var (
	// ErrUnauthenticated means a credential is missing, unknown, revoked or
	// expired (HTTP 401).
	ErrUnauthenticated = errors.New("authentication required: send a valid API token as Authorization: Bearer")
	// ErrForbidden means the caller is known but not allowed (HTTP 403).
	ErrForbidden = errors.New("your role does not allow this")
)

// Secrets are random, so a fast hash is enough to store them.
const (
	tokenSecretPrefix = "omk_"
	shareSecretPrefix = "oms_"
	secretBytes       = 24
	displayPrefixLen  = 12
	touchEvery        = time.Minute
	hoursPerDay       = 24
)

// Credentials is what a request presents: an Authorization bearer value and
// a share-link secret (header or query parameter).
type Credentials struct {
	Bearer string
	Share  string
}

// Principal is who a request was made by, and what they may do.
type Principal struct {
	Actor domain.Actor
	Role  domain.Role
	// Share is set for a share link: read-only access to a fixed set of
	// views.
	Share bool
}

// TokenInput is the request to create an API token.
type TokenInput struct {
	Name    string
	Role    domain.Role
	TTLDays int
}

// LinkInput is the request to create a share link.
type LinkInput struct {
	Label   string
	TTLDays int
}

// CreatedToken is a new token with its secret, which is shown only once.
type CreatedToken struct {
	Secret string           `json:"token"`
	Info   domain.TokenInfo `json:"info"`
}

// CreatedLink is a new share link with its secret, which is shown only once.
type CreatedLink struct {
	Secret string          `json:"token"`
	Info   domain.LinkInfo `json:"info"`
}

// AccessService manages API tokens, share links and the audit log, and
// decides who may do what in a project. A project without tokens is open.
type AccessService struct {
	repo       AccessRepository
	projects   ProjectGetter
	mocks      MockGetter
	adminToken string
	now        func() time.Time
}

// AccessOption configures an AccessService.
type AccessOption func(*AccessService)

// WithAdminToken sets a server-wide token that acts as an admin of every
// project; it is the way back in after a project's tokens are lost.
func WithAdminToken(token string) AccessOption {
	return func(s *AccessService) { s.adminToken = token }
}

// WithAccessClock replaces the clock (for tests).
func WithAccessClock(now func() time.Time) AccessOption {
	return func(s *AccessService) { s.now = now }
}

func NewAccessService(
	repo AccessRepository, projects ProjectGetter, mocks MockGetter, opts ...AccessOption,
) *AccessService {
	s := &AccessService{
		repo: repo, projects: projects, mocks: mocks, adminToken: "",
		now: func() time.Time { return time.Now().UTC() },
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}

func newSecret(prefix string) string {
	buf := make([]byte, secretBytes)
	_, _ = rand.Read(buf)

	return prefix + hex.EncodeToString(buf)
}

func displayPrefix(secret string) string {
	return secret[:min(displayPrefixLen, len(secret))]
}

// Locked reports whether the project requires a token: it has at least one
// token that was not revoked. (An expired token still locks the project, so
// that expiry never opens it.)
func (s *AccessService) Locked(projectID string) bool {
	tokens := s.repo.ListTokens(projectID)
	for i := range tokens {
		if !tokens[i].Revoked {
			return true
		}
	}

	return false
}

func (s *AccessService) isAdminToken(bearer string) bool {
	return s.adminToken != "" && bearer != "" &&
		subtle.ConstantTimeCompare([]byte(bearer), []byte(s.adminToken)) == 1
}

// Authenticate identifies the caller of a project request. A project without
// tokens is open: everybody is an anonymous admin. Otherwise the caller needs
// a token, a share link, or the server's admin token.
func (s *AccessService) Authenticate(projectID string, cred Credentials) (Principal, error) {
	if s.isAdminToken(cred.Bearer) {
		return Principal{
			Actor: domain.Actor{Kind: domain.ActorAdmin, ID: "", Name: domain.ActorAdmin},
			Role:  domain.RoleAdmin, Share: false,
		}, nil
	}

	if !s.Locked(projectID) {
		return Principal{Actor: domain.Anonymous(), Role: domain.RoleAdmin, Share: false}, nil
	}

	shareSecret := cred.Share

	if strings.HasPrefix(cred.Bearer, shareSecretPrefix) {
		shareSecret = cred.Bearer
	} else if cred.Bearer != "" {
		p, ok := s.authToken(projectID, cred.Bearer)
		if ok {
			return p, nil
		}
	}

	if shareSecret != "" {
		p, ok := s.authShare(projectID, shareSecret)
		if ok {
			return p, nil
		}
	}

	return Principal{}, ErrUnauthenticated
}

func (s *AccessService) authToken(projectID, secret string) (Principal, bool) {
	t, ok := s.repo.FindToken(hashSecret(secret))
	now := s.now()

	if !ok || t.ProjectID != projectID || !t.ActiveAt(now) {
		return Principal{}, false
	}

	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
		s.repo.TouchToken(t.ID, now)
	}

	actor := domain.Actor{Kind: domain.ActorToken, ID: t.ID, Name: t.Name}

	return Principal{Actor: actor, Role: t.Role, Share: false}, true
}

func (s *AccessService) authShare(projectID, secret string) (Principal, bool) {
	l, ok := s.repo.FindShareLink(hashSecret(secret))
	if !ok || l.ProjectID != projectID || !l.ActiveAt(s.now()) {
		return Principal{}, false
	}

	return Principal{
		Actor: domain.Actor{Kind: domain.ActorShareLink, ID: l.ID, Name: l.Label}, Role: domain.RoleViewer, Share: true,
	}, true
}

// ResolveShare returns the project a share-link secret opens.
func (s *AccessService) ResolveShare(secret string) (domain.ShareLink, error) {
	l, ok := s.repo.FindShareLink(hashSecret(secret))
	if !ok || !l.ActiveAt(s.now()) {
		return domain.ShareLink{}, domain.ErrNotFound
	}

	return l, nil
}

func (s *AccessService) expiry(ttlDays int) (*time.Time, error) {
	if ttlDays < 0 || ttlDays > domain.MaxTTLDays {
		return nil, domain.AccessError("ttlDays must be between 0 and %d", domain.MaxTTLDays)
	}

	if ttlDays == 0 {
		return nil, nil //nolint:nilnil // no expiry.
	}

	at := s.now().Add(time.Duration(ttlDays) * hoursPerDay * time.Hour)

	return &at, nil
}

// CreateToken makes an API token for the project. The first token of an open
// project must be an admin token, or nobody could manage the project after
// it locks.
func (s *AccessService) CreateToken(projectID string, in TokenInput, actor domain.Actor) (CreatedToken, error) {
	_, err := s.projects.Get(projectID)
	if err != nil {
		return CreatedToken{}, err
	}

	err = s.checkNewToken(projectID, &in)
	if err != nil {
		return CreatedToken{}, err
	}

	expires, err := s.expiry(in.TTLDays)
	if err != nil {
		return CreatedToken{}, err
	}

	secret := newSecret(tokenSecretPrefix)
	t := &domain.AccessToken{
		ID: idgen.New(), ProjectID: projectID, Name: in.Name, Role: in.Role, Prefix: displayPrefix(secret),
		Hash: hashSecret(secret), CreatedAt: s.now(), CreatedBy: actor.Name, ExpiresAt: expires,
		LastUsedAt: nil, Revoked: false,
	}
	s.repo.AddToken(t)

	return CreatedToken{Secret: secret, Info: t.Info(s.now())}, nil
}

func (s *AccessService) checkNewToken(projectID string, in *TokenInput) error {
	err := domain.ValidateLabel("token", in.Name)
	if err != nil {
		return err
	}

	if !in.Role.Valid() {
		return domain.AccessError("role must be one of: viewer, editor, admin")
	}

	live := 0

	tokens := s.repo.ListTokens(projectID)
	for i := range tokens {
		if !tokens[i].Revoked {
			live++
		}
	}

	switch {
	case live >= domain.MaxTokensPerProject:
		return domain.AccessError("at most %d tokens per project", domain.MaxTokensPerProject)
	case live == 0 && in.Role != domain.RoleAdmin:
		return domain.AccessError("the first token of a project must be an admin token")
	default:
		return nil
	}
}

// ListTokens returns the project's tokens, newest first.
func (s *AccessService) ListTokens(projectID string) []domain.TokenInfo {
	tokens := s.repo.ListTokens(projectID)
	now := s.now()
	out := make([]domain.TokenInfo, 0, len(tokens))

	for i := len(tokens) - 1; i >= 0; i-- {
		out = append(out, tokens[i].Info(now))
	}

	return out
}

// RevokeToken disables a token. The last admin token cannot be revoked while
// other tokens remain, or the project could no longer be managed; revoking
// all tokens opens the project again.
func (s *AccessService) RevokeToken(projectID, id string) error {
	t, err := s.repo.GetToken(projectID, id)
	if err != nil {
		return err
	}

	if t.Revoked {
		return nil
	}

	if !s.otherAdminOrNoOthers(projectID, id) {
		return domain.AccessError("this is the last admin token; create another admin token or revoke the others first")
	}

	t.Revoked = true

	return s.repo.UpdateToken(&t)
}

// otherAdminOrNoOthers reports whether revoking the token leaves the project
// either open or with an admin token.
func (s *AccessService) otherAdminOrNoOthers(projectID, revoking string) bool {
	now := s.now()
	remaining := 0

	tokens := s.repo.ListTokens(projectID)
	for i := range tokens {
		t := &tokens[i]
		if t.ID == revoking || t.Revoked {
			continue
		}

		remaining++

		if t.Role == domain.RoleAdmin && t.ActiveAt(now) {
			return true
		}
	}

	return remaining == 0
}

// CreateShareLink makes a read-only share link for the project.
func (s *AccessService) CreateShareLink(projectID string, in LinkInput, actor domain.Actor) (CreatedLink, error) {
	_, err := s.projects.Get(projectID)
	if err != nil {
		return CreatedLink{}, err
	}

	err = domain.ValidateLabel("share link", in.Label)
	if err != nil {
		return CreatedLink{}, err
	}

	live := 0

	links := s.repo.ListShareLinks(projectID)
	for i := range links {
		if !links[i].Revoked {
			live++
		}
	}

	if live >= domain.MaxLinksPerProject {
		return CreatedLink{}, domain.AccessError("at most %d share links per project", domain.MaxLinksPerProject)
	}

	expires, err := s.expiry(in.TTLDays)
	if err != nil {
		return CreatedLink{}, err
	}

	secret := newSecret(shareSecretPrefix)
	l := &domain.ShareLink{
		ID: idgen.New(), ProjectID: projectID, Label: in.Label, Prefix: displayPrefix(secret), Hash: hashSecret(secret),
		CreatedAt: s.now(), CreatedBy: actor.Name, ExpiresAt: expires, Revoked: false,
	}
	s.repo.AddShareLink(l)

	return CreatedLink{Secret: secret, Info: l.Info(s.now())}, nil
}

// ListShareLinks returns the project's share links, newest first.
func (s *AccessService) ListShareLinks(projectID string) []domain.LinkInfo {
	links := s.repo.ListShareLinks(projectID)
	now := s.now()
	out := make([]domain.LinkInfo, 0, len(links))

	for i := len(links) - 1; i >= 0; i-- {
		out = append(out, links[i].Info(now))
	}

	return out
}

// RevokeShareLink disables a share link.
func (s *AccessService) RevokeShareLink(projectID, id string) error {
	l, err := s.repo.GetShareLink(projectID, id)
	if err != nil {
		return err
	}

	l.Revoked = true

	return s.repo.UpdateShareLink(&l)
}

// ProjectOfMock returns the project a mock rule belongs to; found is false
// when there is no such mock.
func (s *AccessService) ProjectOfMock(id string) (projectID string, found bool) {
	if s.mocks == nil {
		return "", false
	}

	m, err := s.mocks.GetMock(id)
	if err != nil {
		return "", false
	}

	return m.ProjectID, true
}

// Record adds an entry to the audit log.
func (s *AccessService) Record(e *domain.AuditEntry) {
	if e.Time.IsZero() {
		e.Time = s.now()
	}

	s.repo.AddAudit(e)
}

// ListAudit returns the project's audit entries, newest first.
func (s *AccessService) ListAudit(projectID string, q *AuditQuery) []domain.AuditEntry {
	return s.repo.ListAudit(projectID, q)
}

// String makes a Principal readable in logs and test failures.
func (p Principal) String() string {
	return fmt.Sprintf("%s(%s)", p.Actor.Kind, p.Actor.Name)
}
