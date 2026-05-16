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

// Public constants — the issuer/audience/PRM URLs are baked in for now.
// When memory-system needs to run on a non-prod host these become config fields.
const (
	// Issuer is the public base URL memory-system serves authlet under. JWTs
	// use this as `iss` and the AS metadata documents are anchored here.
	Issuer = "https://memory-mcp.a11s.dev"

	// PathPrefix is where the AS handler is mounted under Issuer.
	PathPrefix = "/oauth"

	// Audience is the canonical resource URL clients request access for.
	// memory-mcp's Traefik does NOT strip a prefix (unlike hilo), so the
	// public path and the server path are both `/mcp`.
	Audience = Issuer + "/mcp"

	// PRMURL is the absolute URL of the Protected Resource Metadata
	// document for the MCP endpoint (RFC 9728).
	PRMURL = Issuer + "/.well-known/oauth-protected-resource/mcp"

	// upstreamIssuer is the OIDC issuer used to authenticate users
	// upstream. authlet does the discovery against this URL at boot.
	// memory-system federates DIRECTLY to Google (NOT via hilo) — this is
	// memory-system's own Google client.
	upstreamIssuer = "https://accounts.google.com"

	// cleanupInterval is the period for the AS background sweep (expired
	// codes/refresh tokens/idle DCR clients + key rotation check).
	cleanupInterval = time.Hour

	// jwksCacheTTL is how long the JWKS client caches the resolved key set.
	jwksCacheTTL = time.Hour
)

// redirectURI is the upstream OIDC callback this server registers with
// Google. It must match the entry in the GCP OAuth client allow-list.
const redirectURI = Issuer + PathPrefix + "/idp/callback"

// Wiring is the result of Setup — everything the main server needs to
// mount the AS and the bearer-protected MCP endpoint.
type Wiring struct {
	// AS is the assembled authorization server. Its Handler() serves
	// /authorize, /token, /register, /idp/callback, /revoke, /userinfo.
	AS *as.AS

	// BearerMW is the bearer-token middleware to wrap protected handlers
	// (the MCP endpoint). It enforces iss/aud and rejects with a 401 +
	// WWW-Authenticate challenge that points at PRMURL.
	BearerMW func(http.Handler) http.Handler

	// PRMHandler serves the Protected Resource Metadata JSON document.
	PRMHandler http.HandlerFunc

	// RunCleanup starts the AS periodic cleanup goroutine and returns the
	// channel that closes when the goroutine exits (after ctx is canceled).
	RunCleanup func(ctx context.Context) <-chan struct{}
}

// Mount registers the AS and well-known endpoints on a stdlib ServeMux.
// Callers should mount this BEFORE any sub-route that would catch /oauth/*.
//
// memory-system uses stdlib http.ServeMux (Go 1.22+ method-routed) rather
// than chi — the AS's Handler() is still a chi router internally, but we
// serve it under PathPrefix and strip the prefix on the way in so its
// relative routes resolve correctly.
func (w *Wiring) Mount(mux *http.ServeMux) {
	// AS endpoints (/authorize, /token, /register, /idp/callback, /revoke,
	// /userinfo). The trailing "/" makes ServeMux treat PathPrefix as a
	// subtree match.
	mux.Handle(PathPrefix+"/", http.StripPrefix(PathPrefix, w.AS.Handler()))

	// Well-known metadata + JWKS — these are absolute paths on the server,
	// not under PathPrefix. Method-restricted to GET so non-GET requests
	// return 405 (matches hilo's r.Get behavior).
	mux.Handle("GET /.well-known/oauth-authorization-server", http.HandlerFunc(w.AS.MetadataHandler))
	mux.Handle("GET /.well-known/openid-configuration", http.HandlerFunc(w.AS.OIDCMetadataHandler))
	mux.Handle("GET /.well-known/jwks.json", http.HandlerFunc(w.AS.JWKSHandler))
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", w.PRMHandler)
}

// Setup builds the authlet AS, the bearer middleware, the PRM handler,
// and the cleanup goroutine launcher. Caller mounts the routes with
// Wiring.Mount.
//
// Setup reads AUTHLET_MASTER_KEY from the environment (32 bytes,
// base64-encoded). It performs OIDC discovery against Google synchronously
// — if Google is unreachable, Setup returns an error and the caller
// (cmd/server/main.go) treats that as a startup failure.
//
// idTokenClaims looks up email from the tenant_users table on every ID
// token mint. memory-system's MemoryUserResolver returns tenant_id as the
// authlet `sub`, so the lookup is `WHERE tenant_id = ?`. Email is treated
// as verified because the only path that creates a tenant_users row goes
// through a verified Google email (the resolver enforces that).
//
// AdditionalClaims embeds `email` in the access token's custom claims so
// UserContextBridge can populate auth.WithEmail for the JWT path (parity
// with the API-key path).
//
// The logger parameter is plumbed into as.Config.Logger so authlet's
// 500-class and security event logs flow through memory-system's
// structured logger.
func Setup(
	ctx context.Context,
	db *gorm.DB,
	store storage.Storage,
	googleClientID, googleClientSecret string,
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
	if logger == nil {
		logger = slog.Default()
	}

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
		redirectURI,
		[]string{"openid", "email", "profile"},
	)
	if err != nil {
		return nil, fmt.Errorf("upstream oidc: %w", err)
	}

	// idTokenClaims is invoked when the AS mints an ID token (auth request
	// scope contained "openid"). It returns the standard OIDC claims for
	// the user. Errors are logged and we return zero values rather than
	// panic — the resulting ID token will be missing email/name but the
	// access token is unaffected. The email is treated as verified because
	// the only path that creates a tenant_users row goes through a verified
	// Google email (MemoryUserResolver enforces EmailVerified=true).
	idTokenClaims := func(ctx context.Context, userID string) (email string, emailVerified bool, name, picture string) {
		e := lookupTenantEmail(ctx, db, logger, userID)
		if e == "" {
			return "", false, "", ""
		}
		return e, true, "", ""
	}

	// additionalClaims merges the resolved email into the access token's
	// custom claims. UserContextBridge pulls this out of jwt.Claims.Extra
	// to populate auth.WithEmail for JWT-authenticated requests.
	additionalClaims := func(ctx context.Context, userID, _, _ string) map[string]any {
		e := lookupTenantEmail(ctx, db, logger, userID)
		if e == "" {
			return nil
		}
		return map[string]any{"email": e}
	}

	server, err := as.New(as.Config{
		Issuer:              Issuer,
		PathPrefix:          PathPrefix,
		Upstream:            upstream,
		UserResolver:        &MemoryUserResolver{DB: db, Logger: logger},
		Storage:             store,
		KeyManager:          mgr,
		AdditionalClaimsCtx: additionalClaims,
		IDTokenClaimsCtx:    idTokenClaims,
		Logger:              logger,
	})
	if err != nil {
		return nil, fmt.Errorf("as.New: %w", err)
	}

	jwksClient := rs.NewJWKSClient(Issuer+"/.well-known/jwks.json", jwksCacheTTL)
	bearer := rs.Middleware(rs.Config{
		ExpectedIssuer:   Issuer,
		ExpectedAudience: Audience,
		JWKS:             jwksClient,
		ResourceMetadata: PRMURL,
	})

	prm := mcp.PRMHandler(mcp.PRM{
		Resource:               Audience,
		AuthorizationServers:   []string{Issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{"mcp"},
	})

	return &Wiring{
		AS:         server,
		BearerMW:   bearer,
		PRMHandler: prm,
		RunCleanup: func(ctx context.Context) <-chan struct{} {
			return server.RunCleanup(ctx, cleanupInterval)
		},
	}, nil
}

// lookupTenantEmail returns the email of any tenant_user row matching
// tenantID, or the empty string when none exists or on any DB error.
// Errors are logged at warn; callers map a "" result to a missing claim
// (idTokenClaims returns zero values; additionalClaims returns nil).
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

// loadMasterKey reads and hex-decodes AUTHLET_MASTER_KEY. The decoded value
// MUST be exactly 32 bytes (64 hex chars) — jwt.NewManager uses it to seal
// the stored signing-key blob with AES-256-GCM. Generate with
// `openssl rand -hex 32`.
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
