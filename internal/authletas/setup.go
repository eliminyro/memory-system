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
// is a startup failure. The JWT `sub` is the per-user tenant_users.id (set by
// MemoryUserResolver). idTokenClaims/additionalClaims look the email up by that
// unique id, and additionalClaims also embeds a signed `tenant_id` claim so
// UserContextBridge can attach tenant+subject+email from the token alone — no DB
// read on the request path. Email is treated as verified since a tenant_users
// row only exists via a verified Google email, giving the JWT path parity with
// the API-key path. logger flows to as.Config.Logger.
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

	// idTokenClaims runs when the AS mints an ID token. userID is the
	// tenant_users.id (`sub`); the email is looked up by that unique id. A lookup
	// miss returns zero values (ID token loses email/name; access token
	// unaffected). Email is verified because a tenant_users row only exists via a
	// verified Google email (MemoryUserResolver enforces EmailVerified=true).
	idTokenClaims := func(ctx context.Context, userID string) (email string, emailVerified bool, name, picture string) {
		e, _, found := lookupUserClaims(ctx, db, logger, userID)
		if !found {
			return "", false, "", ""
		}
		return e, true, "", ""
	}

	// additionalClaims puts the resolved email AND tenant_id in the access
	// token's custom claims — both read from the SAME tenant_users row (by unique
	// id, no LIMIT ambiguity). UserContextBridge reads them from jwt.Claims.Extra
	// into auth.WithEmail / auth.WithTenantID, so the JWT path needs no DB read.
	additionalClaims := func(ctx context.Context, userID, _, _ string) map[string]any {
		email, tenantID, found := lookupUserClaims(ctx, db, logger, userID)
		if !found {
			return nil
		}
		return map[string]any{"email": email, "tenant_id": tenantID}
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
		prmURL: urls.prmURL,
	}, nil
}

// lookupUserClaims returns the email and tenant_id for the tenant_users row
// keyed by its unique id (the JWT `sub`). found is false when no row matches or
// on any DB error (logged at warn); callers map that to a missing claim. Keying
// on the unique id — never `tenant_id` with LIMIT 1 — is what makes the claim
// unambiguous in a shared tenant with multiple members.
func lookupUserClaims(ctx context.Context, db *gorm.DB, logger *slog.Logger, userID string) (email, tenantID string, found bool) {
	var row struct {
		Email    string `gorm:"column:email"`
		TenantID string `gorm:"column:tenant_id"`
	}
	if err := db.WithContext(ctx).
		Table("tenant_users").
		Select("email", "tenant_id").
		Where("id = ?", userID).
		Limit(1).
		Scan(&row).Error; err != nil {
		logger.Warn("authletas: tenant_user email lookup failed",
			"id", userID, "err", err)
		return "", "", false
	}
	if row.Email == "" {
		return "", "", false
	}
	return row.Email, row.TenantID, true
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
