package codex

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/credpool"
)

const (
	selectionFirstAvailable  = "first-available"
	selectionRoundRobin      = "round-robin"
	selectionSessionAffinity = "session-affinity"
)

type affinityEntry struct {
	accountIdx int
	boundAt    time.Time
}

type managedAccount struct {
	poolID       string
	ID           string
	AccessToken  string
	RefreshToken string
	FilePath     string
	PlanType     string
}

type accountMeta struct {
	poolID       string
	ID           string
	RefreshToken string
	FilePath     string
	PlanType     string
}

type accountStore struct {
	mu            sync.Mutex
	pool          *credpool.Pool
	meta          []accountMeta
	strategy      string
	rrIndex       int
	fallback      time.Duration
	affinityTTL   time.Duration
	affinityMax   int
	affinity      map[string]affinityEntry
	affinityOrder []string
	now           func() time.Time
}

func newAccountStore(cfg Config) (*accountStore, error) {
	path := strings.TrimSpace(cfg.ManagedOAuthStoragePath)
	if path == "" {
		return nil, fmt.Errorf("%s: managed_oauth_storage_path is empty", ID)
	}
	accounts, err := loadManagedAccounts(path, cfg.ManagedOAuthAccounts)
	if err != nil {
		return nil, err
	}
	creds := make([]credpool.Credential, len(accounts))
	meta := make([]accountMeta, len(accounts))
	for i, a := range accounts {
		poolID := a.FilePath
		creds[i] = credpool.Credential{
			ID:              poolID,
			Secret:          a.AccessToken,
			RemoteAccountID: a.ID,
		}
		meta[i] = accountMeta{
			poolID:       poolID,
			ID:           a.ID,
			RefreshToken: a.RefreshToken,
			FilePath:     a.FilePath,
			PlanType:     a.PlanType,
		}
	}
	pool, err := credpool.New(creds)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ID, err)
	}
	strategy := strings.TrimSpace(cfg.ManagedOAuthSelectionStrategy)
	if strategy == "" {
		strategy = selectionFirstAvailable
	}
	fallback := cfg.RateLimitFallback
	if fallback <= 0 {
		fallback = 60 * time.Second
	}
	return &accountStore{
		pool:          pool,
		meta:          meta,
		strategy:      strategy,
		rrIndex:       0,
		fallback:      fallback,
		affinityTTL:   cfg.ManagedOAuthSessionAffinityTTL,
		affinityMax:   cfg.ManagedOAuthSessionAffinityMaxEntries,
		affinity:      make(map[string]affinityEntry),
		affinityOrder: []string{},
		now:           time.Now,
	}, nil
}
