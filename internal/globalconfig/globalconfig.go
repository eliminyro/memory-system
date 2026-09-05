// Package globalconfig serves the runtime global config as a lock-free atomic
// snapshot loaded from the instance_config singleton, shared by the service and
// server layers. Load at boot; refresh after every write (write-through).
package globalconfig

import (
	"context"
	"sync/atomic"

	"github.com/eliminyro/memory-system/internal/models"
)

// Loader fetches the singleton instance_config row.
// *repository.InstanceConfigRepository satisfies it.
type Loader interface {
	Get(ctx context.Context) (*models.InstanceConfig, error)
}

// Snapshot is an immutable typed view of the globals, swapped atomically on refresh.
type Snapshot struct {
	MMRLambda             float64
	StalenessPenalty      float64
	CandidatePool         int
	SnippetChars          int
	HistoryRetentionDays  int
	HistoryEnabled        bool
	StalenessDefault      string
	DuplicateGuardDefault bool
	CleanupScanDefault    bool
	DuplicateThreshold    float64
	SelfServicePolicy     string
	SignupDomains         string
	AdminEmails           string
	CleanupEnabled        bool
	CleanupIntervalHours  int
	RetentionSweepEnabled bool
	RetentionGraceDays    int
	MetricsRetentionDays  int
	RateLimitRPS          float64
	RateLimitBurst        int
	TrustedProxyDepth     int
	MaxRequestBytes       int64
	LogLevel              string
	WebhookURL            string
	RequireConfigListener bool
}

// Accessor holds the current global-config snapshot behind an atomic pointer.
type Accessor struct {
	loader Loader
	snap   atomic.Pointer[Snapshot]
}

// New returns an accessor seeded with a zero snapshot; call Load before reads.
func New(loader Loader) *Accessor {
	a := &Accessor{loader: loader}
	a.snap.Store(&Snapshot{})
	return a
}

// Load reads instance_config and swaps in a fresh snapshot.
func (a *Accessor) Load(ctx context.Context) error {
	cfg, err := a.loader.Get(ctx)
	if err != nil {
		return err
	}
	a.snap.Store(snapshotFrom(cfg))
	return nil
}

// Refresh reloads the snapshot after a write (write-through invalidation).
func (a *Accessor) Refresh(ctx context.Context) error { return a.Load(ctx) }

// Snapshot returns the current snapshot; never nil after New.
func (a *Accessor) Snapshot() *Snapshot { return a.snap.Load() }

func snapshotFrom(c *models.InstanceConfig) *Snapshot {
	return &Snapshot{
		MMRLambda:             c.MMRLambda,
		StalenessPenalty:      c.StalenessPenalty,
		CandidatePool:         c.CandidatePool,
		SnippetChars:          c.SnippetChars,
		HistoryRetentionDays:  c.HistoryRetentionDays,
		HistoryEnabled:        c.HistoryEnabled,
		StalenessDefault:      c.StalenessDefault,
		DuplicateGuardDefault: c.DuplicateGuardDefault,
		CleanupScanDefault:    c.CleanupScanDefault,
		DuplicateThreshold:    c.DuplicateThreshold,
		SelfServicePolicy:     c.SelfServicePolicy,
		SignupDomains:         c.SignupDomains,
		AdminEmails:           c.AdminEmails,
		CleanupEnabled:        c.CleanupEnabled,
		CleanupIntervalHours:  c.CleanupIntervalHours,
		RetentionSweepEnabled: c.RetentionSweepEnabled,
		RetentionGraceDays:    c.RetentionGraceDays,
		MetricsRetentionDays:  c.MetricsRetentionDays,
		RateLimitRPS:          c.RateLimitRPS,
		RateLimitBurst:        c.RateLimitBurst,
		TrustedProxyDepth:     c.TrustedProxyDepth,
		MaxRequestBytes:       c.MaxRequestBytes,
		LogLevel:              c.LogLevel,
		WebhookURL:            c.WebhookURL,
		RequireConfigListener: c.RequireConfigListener,
	}
}

// Typed getters — each reads the live snapshot lock-free.
func (a *Accessor) MMRLambda() float64          { return a.Snapshot().MMRLambda }
func (a *Accessor) StalenessPenalty() float64   { return a.Snapshot().StalenessPenalty }
func (a *Accessor) CandidatePool() int          { return a.Snapshot().CandidatePool }
func (a *Accessor) SnippetChars() int           { return a.Snapshot().SnippetChars }
func (a *Accessor) HistoryRetentionDays() int   { return a.Snapshot().HistoryRetentionDays }
func (a *Accessor) HistoryEnabled() bool        { return a.Snapshot().HistoryEnabled }
func (a *Accessor) StalenessDefault() string    { return a.Snapshot().StalenessDefault }
func (a *Accessor) DuplicateGuardDefault() bool { return a.Snapshot().DuplicateGuardDefault }
func (a *Accessor) CleanupScanDefault() bool    { return a.Snapshot().CleanupScanDefault }
func (a *Accessor) DuplicateThreshold() float64 { return a.Snapshot().DuplicateThreshold }
func (a *Accessor) SelfServicePolicy() string   { return a.Snapshot().SelfServicePolicy }
func (a *Accessor) SignupDomains() string       { return a.Snapshot().SignupDomains }
func (a *Accessor) AdminEmails() string         { return a.Snapshot().AdminEmails }
func (a *Accessor) CleanupEnabled() bool        { return a.Snapshot().CleanupEnabled }
func (a *Accessor) CleanupIntervalHours() int   { return a.Snapshot().CleanupIntervalHours }
func (a *Accessor) RetentionSweepEnabled() bool { return a.Snapshot().RetentionSweepEnabled }
func (a *Accessor) RetentionGraceDays() int     { return a.Snapshot().RetentionGraceDays }
func (a *Accessor) MetricsRetentionDays() int   { return a.Snapshot().MetricsRetentionDays }
func (a *Accessor) RateLimitRPS() float64       { return a.Snapshot().RateLimitRPS }
func (a *Accessor) RateLimitBurst() int         { return a.Snapshot().RateLimitBurst }
func (a *Accessor) TrustedProxyDepth() int      { return a.Snapshot().TrustedProxyDepth }
func (a *Accessor) MaxRequestBytes() int64      { return a.Snapshot().MaxRequestBytes }
func (a *Accessor) LogLevel() string            { return a.Snapshot().LogLevel }
func (a *Accessor) WebhookURL() string          { return a.Snapshot().WebhookURL }
func (a *Accessor) RequireConfigListener() bool { return a.Snapshot().RequireConfigListener }
