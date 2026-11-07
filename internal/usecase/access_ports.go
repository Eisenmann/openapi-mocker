package usecase

import (
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// AuditQuery filters the audit log. Zero values match everything.
type AuditQuery struct {
	Limit  int
	Action string
	Actor  string
	Since  time.Time
}

// Matches reports whether an entry passes the query's filters (not its limit).
func (q *AuditQuery) Matches(e *domain.AuditEntry) bool {
	switch {
	case q.Action != "" && e.Action != q.Action:
		return false
	case q.Actor != "" && e.Actor.Name != q.Actor && e.Actor.ID != q.Actor:
		return false
	default:
		return q.Since.IsZero() || !e.Time.Before(q.Since)
	}
}

// TokenRepository stores API tokens. Find, Get and List return copies:
// changing one changes nothing until it is passed to Update.
type TokenRepository interface {
	AddToken(t *domain.AccessToken)
	FindToken(hash string) (domain.AccessToken, bool)
	GetToken(projectID, id string) (domain.AccessToken, error)
	UpdateToken(t *domain.AccessToken) error
	// TouchToken records the last use of a token without touching anything else.
	TouchToken(id string, at time.Time)
	ListTokens(projectID string) []domain.AccessToken
}

// ShareLinkRepository stores share links (copy semantics like TokenRepository).
type ShareLinkRepository interface {
	AddShareLink(l *domain.ShareLink)
	FindShareLink(hash string) (domain.ShareLink, bool)
	GetShareLink(projectID, id string) (domain.ShareLink, error)
	UpdateShareLink(l *domain.ShareLink) error
	ListShareLinks(projectID string) []domain.ShareLink
}

// AuditRepository stores the audit log.
type AuditRepository interface {
	AddAudit(e *domain.AuditEntry)
	// ListAudit returns the project's entries, newest first.
	ListAudit(projectID string, q *AuditQuery) []domain.AuditEntry
}

// AccessRepository stores the API tokens, share links and audit log of the
// projects.
type AccessRepository interface {
	TokenRepository
	ShareLinkRepository
	AuditRepository
}

// MockGetter finds a mock rule, to tell which project a mock belongs to.
type MockGetter interface {
	GetMock(id string) (*domain.MockRule, error)
}
