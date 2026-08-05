package authletas

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/eliminyro/authlet/pkg/as"
	"github.com/eliminyro/authlet/pkg/idp"
	"github.com/eliminyro/authlet/pkg/jwt"
	"github.com/eliminyro/authlet/pkg/mcp"
	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

const (
	// PathPrefix is where the AS handler is mounted under the public base URL.
	PathPrefix = "/oauth"

	// upstreamIssuer is the OIDC issuer for upstream user auth; authlet
	// discovers against it at boot. memory-system federates directly to
	// Google (its own Google client).
	upstreamIssuer = "https://accounts.google.com"

	// cleanupInterval is the AS background sweep period (expired codes/tokens/
	// idle DCR clients + key rotation check).
	cleanupInterval = time.Hour

	// jwksCacheTTL is how long the JWKS client caches the resolved key set.
	jwksCacheTTL = time.Hour
)

// asURLs holds the public authlet URLs derived from PUBLIC_BASE_URL, kept in
// one place to keep the deployment host out of the source tree.
type asURLs struct {
	// issuer is the public base URL; JWT `iss` and AS metadata anchor here.
	issuer string
	// audience is the canonical resource URL clients request access for. No
	// prefix strip, so public and server paths are both `/mcp`.
	audience string
	// prmURL is the absolute Protected Resource Metadata URL for /mcp (RFC 9728).
	prmURL string
	// redirectURI is the upstream OIDC callback; must match the IdP's OAuth
	// client allow-list entry.
	redirectURI string
}

// deriveURLs builds the authlet URL set from the public base URL (already
// validated + trailing-slash-trimmed by config.Load).
func deriveURLs(baseURL string) asURLs {
	return asURLs{
		issuer:      baseURL,
		audience:    baseURL + "/mcp",
		prmURL:      baseURL + "/.well-known/oauth-protected-resource/mcp",
		redirectURI: baseURL + PathPrefix + "/idp/callback",
	}
}

// Wiring is the result of Setup — everything the main server needs to
// mount the AS and the bearer-protected MCP endpoint.
type Wiring struct {
	// AS is the assembled authorization server (Handler() serves /authorize,
	// /token, /register, /idp/callback, /revoke, /userinfo).
	AS *as.AS

	// Resolver is the MemoryUserResolver wired into the AS. The composition
	// root sets wiring.Resolver.Provision post-Setup to enable auto-provisioning
	// on a tenant_users miss (nil Provision preserves the reject-by-default
	// behavior). Exposed so the callback can be injected without changing
	// Setup's signature.
	Resolver *MemoryUserResolver

	// BearerMW wraps protected handlers (the MCP endpoint): enforces iss/aud,
	// rejects with 401 + WWW-Authenticate challenge pointing at prmURL.
	BearerMW func(http.Handler) http.Handler

	// PRMHandler serves the Protected Resource Metadata JSON document.
	PRMHandler http.HandlerFunc

	// RunCleanup starts the AS cleanup goroutine; the returned channel closes
	// when it exits (after ctx is canceled).
	RunCleanup func(ctx context.Context) <-chan struct{}

	// db backs UserContextBridge's verified-email -> tenant_users.id lookup so
	// the JWT path can attach a unified auth.Subject. Nil in unit tests: the
	// bridge attaches only tenant id + email (no subject) and Pass 2 fails closed.
	db *gorm.DB

	// prmURL is the absolute PRM URL; WWWAuth401 embeds it in the Bearer
	// challenge so OAuth-discovering clients find the AS. Empty in unit-test
	// Wiring (challenge then omits resource_metadata).
	prmURL string
}

// Mount registers the AS and well-known endpoints on a stdlib ServeMux; call
// before any sub-route that would catch /oauth/*. The AS Handler() is a chi
// router served under PathPrefix with the prefix stripped so its relative
// routes resolve.
func (w *Wiring) Mount(mux *http.ServeMux) {
	// Trailing "/" makes ServeMux treat PathPrefix as a subtree match.
	mux.Handle(PathPrefix+"/", http.StripPrefix(PathPrefix, w.AS.Handler()))

	// Well-known metadata + JWKS: absolute server paths, not under PathPrefix.
	// GET-restricted so non-GET returns 405.
	mux.Handle("GET /.well-known/oauth-authorization-server", http.HandlerFunc(w.AS.MetadataHandler))
	mux.Handle("GET /.well-known/openid-configuration", http.HandlerFunc(w.AS.OIDCMetadataHandler))
	mux.Handle("GET /.well-known/jwks.json", http.HandlerFunc(w.AS.JWKSHandler))
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", w.PRMHandler)
}

// Setup builds the authlet AS, bearer middleware, PRM handler, and cleanup
// launcher; caller mounts routes with Wiring.Mount. Reads AUTHLET_MASTER_KEY
// from env and does synchronous Google OIDC discovery — an unreachable Google
// is a startup failure. idTokenClaims/AdditionalClaims embed the resolved
// email (looked up by tenant_id `sub`; treated as verified since a
// tenant_users row only exists via a verified Google email) so the JWT path
// reaches parity with the API-key path. logger flows to as.Config.Logger.
func Setup(
	ctx context.Context,
	db *gorm.DB,
	store storage.Storage,
	googleClientID, googleClientSecret, baseURL string,
	logger *slog.Logger,
) (*Wiring, error) {
	if db == nil {
		return nil, errors.New("authletas: db is required")
	}
	if store == nil {
		return nil, errors.New("authletas: store is required")
	}
	if googleClientID == "" || googleClientSecret == "" {
		return nil, errors.New("authletas: GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required")
	}
	if baseURL == "" {
		return nil, errors.New("authletas: baseURL (PUBLIC_BASE_URL) is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Anchor issuer/audience/PRM/redirect at the deployment's own base URL.
	urls := deriveURLs(baseURL)

	masterKey, err := loadMasterKey()
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}

	mgr := jwt.NewManager(store.SigningKeys(), masterKey)
	if err := mgr.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("jwt bootstrap: %w", err)
	}

	upstream, err := idp.NewOIDC(
		ctx,
		upstreamIssuer,
		googleClientID,
		googleClientSecret,
		urls.redirectURI,
		[]string{"openid", "email", "profile"},
	)
	if err != nil {
		return nil, fmt.Errorf("upstream oidc: %w", err)
	}

	// idTokenClaims runs when the AS mints an ID token. A lookup miss returns
	// zero values (ID token loses email/name; access token unaffected). Email
	// is verified because a tenant_users row only exists via a verified Google
	// email (MemoryUserResolver enforces EmailVerified=true).
	idTokenClaims := func(ctx context.Context, userID string) (email string, emailVerified bool, name, picture string) {
		e := lookupTenantEmail(ctx, db, logger, userID)
		if e == "" {
			return "", false, "", ""
		}
		return e, true, "", ""
	}

	// additionalClaims puts the resolved email in the access token's custom
	// claims; UserContextBridge reads it from jwt.Claims.Extra into auth.WithEmail.
	additionalClaims := func(ctx context.Context, userID, _, _ string) map[string]any {
		e := lookupTenantEmail(ctx, db, logger, userID)
		if e == "" {
			return nil
		}
		return map[string]any{"email": e}
	}

	// Construct the resolver as a local so it can be exposed on Wiring; the
	// composition root sets .Provision on it after Setup returns.
	resolver := &MemoryUserResolver{DB: db, Logger: logger}

	server, err := as.New(as.Config{
		Issuer:              urls.issuer,
		PathPrefix:          PathPrefix,
		Upstream:            upstream,
		UserResolver:        resolver,
		Storage:             store,
		KeyManager:          mgr,
		AdditionalClaimsCtx: additionalClaims,
		IDTokenClaimsCtx:    idTokenClaims,
		Logger:              logger,
	})
	if err != nil {
		return nil, fmt.Errorf("as.New: %w", err)
	}

	jwksClient := rs.NewJWKSClient(urls.issuer+"/.well-known/jwks.json", jwksCacheTTL)
	bearer := rs.Middleware(rs.Config{
		ExpectedIssuer:   urls.issuer,
		ExpectedAudience: urls.audience,
		JWKS:             jwksClient,
		ResourceMetadata: urls.prmURL,
	})

	prm := mcp.PRMHandler(mcp.PRM{
		Resource:               urls.audience,
		AuthorizationServers:   []string{urls.issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{"mcp"},
	})

	return &Wiring{
		AS:         server,
		Resolver:   resolver,
		BearerMW:   bearer,
		PRMHandler: prm,
		RunCleanup: func(ctx context.Context) <-chan struct{} {
			return server.RunCleanup(ctx, cleanupInterval)
		},
		db:     db,
		prmURL: urls.prmURL,
	}, nil
}

// lookupTenantEmail returns the email for a tenant_user row matching tenantID,
// or "" when none exists or on any DB error (logged at warn). Callers map ""
// to a missing claim.
func lookupTenantEmail(ctx context.Context, db *gorm.DB, logger *slog.Logger, tenantID string) string {
	var row struct {
		Email string `gorm:"column:email"`
	}
	if err := db.WithContext(ctx).
		Table("tenant_users").
		Select("email").
		Where("tenant_id = ?", tenantID).
		Limit(1).
		Scan(&row).Error; err != nil {
		logger.Warn("authletas: tenant_user email lookup failed",
			"tenant_id", tenantID, "err", err)
		return ""
	}
	return row.Email
}

// lookupTenantUserID returns the tenant_users.id (the unified subject id for
// the JWT human path) for a verified email. Returns ("", false) on no row or
// DB error (logged at warn), so the bridge attaches no subject and Pass 2
// fails closed.
func lookupTenantUserID(ctx context.Context, db *gorm.DB, logger *slog.Logger, email string) (string, bool) {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Table("tenant_users").
		Select("id").
		Where("email = ?", email).
		Limit(1).
		Scan(&row).Error; err != nil {
		logger.Warn("authletas: tenant_user id lookup failed", "err", err)
		return "", false
	}
	if row.ID == "" {
		return "", false
	}
	return row.ID, true
}

// loadMasterKey reads and hex-decodes AUTHLET_MASTER_KEY, which must be exactly
// 32 bytes — jwt.NewManager uses it to seal the signing-key blob (AES-256-GCM).
// Generate with `openssl rand -hex 32`.
func loadMasterKey() ([]byte, error) {
	h := os.Getenv("AUTHLET_MASTER_KEY")
	if h == "" {
		return nil, errors.New("AUTHLET_MASTER_KEY env var not set")
	}
	k, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("AUTHLET_MASTER_KEY not valid hex: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("AUTHLET_MASTER_KEY must decode to 32 bytes, got %d", len(k))
	}
	return k, nil
}
