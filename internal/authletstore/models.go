// Package authletstore is memory-system's GORM-backed implementation of
// authlet/pkg/storage. Tables match the migration in
// internal/migrations/<ts>_authlet_tables.sql.
package authletstore

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// OAuthClient is the GORM row for a registered OAuth client. Both public
// (DCR'd) and pre-registered confidential clients are stored here;
// ClientSecretHash and TokenEndpointAuthMethod are top-level columns rather
// than inside Metadata so confidential clients seeded by SQL can be looked
// up without JSON unmarshalling.
type OAuthClient struct {
	ClientID                string         `gorm:"primaryKey;column:client_id"`
	ClientSecretHash        []byte         `gorm:"column:client_secret_hash"`
	TokenEndpointAuthMethod string         `gorm:"column:token_endpoint_auth_method"`
	RedirectURIs            StringArray    `gorm:"column:redirect_uris;type:text[]"`
	Metadata                datatypes.JSON `gorm:"column:metadata"`
	CreatedAt               time.Time      `gorm:"column:created_at"`
	LastUsedAt              time.Time      `gorm:"column:last_used_at"`
	ExpiresAt               time.Time      `gorm:"column:expires_at"`
}

// TableName returns the Postgres table name for OAuthClient.
func (OAuthClient) TableName() string { return "oauth_clients" }

// OAuthCode is the GORM row for a short-lived authorization code. The code
// itself is never stored; only its hash is.
type OAuthCode struct {
	CodeHash      string    `gorm:"primaryKey;column:code_hash"`
	ClientID      string    `gorm:"column:client_id;index"`
	UserID        string    `gorm:"column:user_id"`
	Resource      string    `gorm:"column:resource"`
	Scope         string    `gorm:"column:scope"`
	PKCEChallenge string    `gorm:"column:pkce_challenge"`
	PKCEMethod    string    `gorm:"column:pkce_method"`
	RedirectURI   string    `gorm:"column:redirect_uri"`
	ExpiresAt     time.Time `gorm:"column:expires_at;index"`
}

// TableName returns the Postgres table name for OAuthCode.
func (OAuthCode) TableName() string { return "oauth_codes" }

// OAuthRefreshToken is the GORM row for a refresh token. Reuse-detection
// state is tracked via ReplacedBy; FamilyID groups rotated tokens so the
// whole chain can be revoked.
type OAuthRefreshToken struct {
	TokenHash  string         `gorm:"primaryKey;column:token_hash"`
	FamilyID   string         `gorm:"column:family_id;index"`
	ClientID   string         `gorm:"column:client_id;index"`
	UserID     string         `gorm:"column:user_id;index"`
	Resource   string         `gorm:"column:resource"`
	Scope      string         `gorm:"column:scope"`
	ReplacedBy string         `gorm:"column:replaced_by"`
	RevokedAt  gorm.DeletedAt `gorm:"column:revoked_at"`
	ExpiresAt  time.Time      `gorm:"column:expires_at;index"`
}

// TableName returns the Postgres table name for OAuthRefreshToken.
func (OAuthRefreshToken) TableName() string { return "oauth_refresh_tokens" }

// FamilyRevocation is a separate row indicating an entire token family has
// been revoked. Used by RevokeFamily / IsFamilyRevoked.
type FamilyRevocation struct {
	FamilyID  string    `gorm:"primaryKey;column:family_id"`
	RevokedAt time.Time `gorm:"column:revoked_at"`
}

// TableName returns the Postgres table name for FamilyRevocation.
func (FamilyRevocation) TableName() string { return "oauth_revoked_families" }

// AuthletSigningKey is the GORM row for an RSA signing key. Only one key
// is active at a time; older keys remain queryable for verification until
// RetiresAt passes.
type AuthletSigningKey struct {
	ID                  string     `gorm:"primaryKey;column:id"`
	Algorithm           string     `gorm:"column:alg"`
	PublicPEM           []byte     `gorm:"column:public_pem"`
	PrivatePEMEncrypted []byte     `gorm:"column:private_pem_encrypted"`
	IsActive            bool       `gorm:"column:is_active;index"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	RetiresAt           *time.Time `gorm:"column:retires_at"`
}

// TableName returns the Postgres table name for AuthletSigningKey.
func (AuthletSigningKey) TableName() string { return "authlet_signing_keys" }
