package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role is what an API token may do in its project.
type Role string

const (
	// RoleViewer may read the project.
	RoleViewer Role = "viewer"
	// RoleEditor may also change contracts, mocks, state and auth settings.
	RoleEditor Role = "editor"
	// RoleAdmin may also manage tokens and share links and change or delete
	// the project.
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r.rank() > 0
}

// Allows reports whether a holder of r may do what need permits.
func (r Role) Allows(need Role) bool {
	return r.rank() >= need.rank() && need.rank() > 0
}

func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2 //nolint:mnd // viewer < editor < admin.
	case RoleAdmin:
		return 3 //nolint:mnd // viewer < editor < admin.
	default:
		return 0
	}
}

// Limits of the access settings.
const (
	MaxTokensPerProject = 100
	MaxLinksPerProject  = 100
	MaxLabelLen         = 80
	MaxTTLDays          = 3650
)

// ErrInvalidAccess wraps every access-settings validation failure.
var ErrInvalidAccess = errors.New("invalid access settings")

// AccessError builds an ErrInvalidAccess with details.
func AccessError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccess, fmt.Sprintf(format, args...))
}

// ValidateLabel checks the name of a token or the label of a share link.
func ValidateLabel(kind, label string) error {
	switch {
	case strings.TrimSpace(label) == "":
		return AccessError("%s name is required", kind)
	case len(label) > MaxLabelLen:
		return AccessError("%s name is longer than %d characters", kind, MaxLabelLen)
	default:
		return nil
	}
}

// AccessToken is an API token of one project. Only the SHA-256 hash of the
// secret is stored; the secret itself is shown once, when the token is made.
type AccessToken struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Role      Role   `json:"role"`
	// Prefix is the start of the secret, to recognize a token in a list.
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"hash"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Revoked    bool       `json:"revoked"`
}

// ActiveAt reports whether the token can authenticate at the given time.
func (t *AccessToken) ActiveAt(now time.Time) bool {
	return !t.Revoked && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
}

// TokenInfo is a token as the API shows it: without the hash.
type TokenInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	CreatedBy  string     `json:"createdBy"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Revoked    bool       `json:"revoked"`
	Expired    bool       `json:"expired"`
}

// Info returns the API view of the token.
func (t *AccessToken) Info(now time.Time) TokenInfo {
	return TokenInfo{
		ID: t.ID, Name: t.Name, Role: t.Role, Prefix: t.Prefix, CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, Revoked: t.Revoked,
		Expired: t.ExpiresAt != nil && !now.Before(*t.ExpiresAt),
	}
}

// ShareLink gives read-only access to a snapshot of a project (contract and
// mocks) to anybody who has the link.
type ShareLink struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Label     string     `json:"label"`
	Prefix    string     `json:"prefix"`
	Hash      string     `json:"hash"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy string     `json:"createdBy"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Revoked   bool       `json:"revoked"`
}

// ActiveAt reports whether the link works at the given time.
func (l *ShareLink) ActiveAt(now time.Time) bool {
	return !l.Revoked && (l.ExpiresAt == nil || now.Before(*l.ExpiresAt))
}

// LinkInfo is a share link as the API shows it: without the hash.
type LinkInfo struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy string     `json:"createdBy"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Revoked   bool       `json:"revoked"`
	Expired   bool       `json:"expired"`
}

// Info returns the API view of the link.
func (l *ShareLink) Info(now time.Time) LinkInfo {
	return LinkInfo{
		ID: l.ID, Label: l.Label, Prefix: l.Prefix, CreatedAt: l.CreatedAt, CreatedBy: l.CreatedBy,
		ExpiresAt: l.ExpiresAt, Revoked: l.Revoked, Expired: l.ExpiresAt != nil && !now.Before(*l.ExpiresAt),
	}
}

// Kinds of actors in the audit log.
const (
	ActorAnonymous = "anonymous"
	ActorToken     = "token"
	ActorAdmin     = "admin"
	ActorShareLink = "share-link"
)

// Actor is who did something.
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// Anonymous is the actor of a request to a project without tokens.
func Anonymous() Actor {
	return Actor{Kind: ActorAnonymous, ID: "", Name: ActorAnonymous}
}

// AuditEntry records one change to a project (or a refused attempt).
type AuditEntry struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	ProjectID  string    `json:"projectId"`
	Actor      Actor     `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType,omitempty"`
	TargetID   string    `json:"targetId,omitempty"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	Note       string    `json:"note,omitempty"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
}
