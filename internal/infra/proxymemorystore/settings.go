package proxymemorystore

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
)

// The owner-scoped settings persistence of design §4: the owner's stored,
// unprojected per-phase overrides and the trusted write-time dependency
// context that travels with them.
//
// The rows live in the isolated feature persistence of the proxy-memory
// family, never in the core continuity database: the continuity database has
// a single connection and one writer, and a settings write on it would hold
// the connection the mandatory core path needs.
//
// The owner key holds the tenant and the principal only. There is no project
// column, so the settings are global for that owner and apply to every
// project of that owner.

// settingsRow is one stored phase row of the owner's settings.
//
// The dependency columns are the trusted write-time context. A legacy row
// carries the added columns' defaults, so its context is absent rather than
// backfilled: the migration records no history it did not observe.
type settingsRow struct {
	Phase             string `bun:"phase"`
	Backend           string `bun:"backend"`
	Model             string `bun:"model"`
	ReasoningEffort   string `bun:"reasoning_effort"`
	DependencyPresent int    `bun:"dependency_present"`
	ContextBackend    string `bun:"dependency_backend"`
	ContextModel      string `bun:"dependency_model"`
	InheritedBackend  int    `bun:"inherited_backend"`
	InheritedModel    int    `bun:"inherited_model"`
}

// storedPhase maps one stored row onto the feature's per-phase state.
func (r settingsRow) storedPhase() proxymemory.StoredPhaseSettings {
	return proxymemory.StoredPhaseSettings{
		Overrides: proxymemory.TargetOverrides{
			Backend:         r.Backend,
			Model:           r.Model,
			ReasoningEffort: r.ReasoningEffort,
		},
		Dependency: proxymemory.DependencyContext{
			Present:          r.DependencyPresent != 0,
			Backend:          r.ContextBackend,
			Model:            r.ContextModel,
			InheritedBackend: r.InheritedBackend != 0,
			InheritedModel:   r.InheritedModel != 0,
		},
	}
}

// ReadSettings returns the owner's stored per-phase settings: the raw
// unprojected overrides and their trusted write-time dependency context.
//
// The read is owner-scoped: only the rows of the caller's own tenant and
// principal are selected, so no other owner's settings are ever read
// (Requirement 2.1). An owner who never wrote one has empty settings rather
// than a missing row: the settings of such an owner are the operator
// defaults.
func (s *Store) ReadSettings(ctx context.Context, p proxymemory.Principal) (proxymemory.StoredOwnerSettings, error) {
	var rows []settingsRow
	err := s.db.NewRaw(`
		SELECT phase, backend, model, reasoning_effort,
			dependency_present, dependency_backend, dependency_model,
			inherited_backend, inherited_model
		FROM proxy_memory_settings
		WHERE tenant_id = ? AND principal_id = ?
	`, p.TenantID, p.PrincipalID).Scan(ctx, &rows)
	if err != nil {
		return proxymemory.StoredOwnerSettings{}, opErr("select settings", err)
	}
	var stored proxymemory.StoredOwnerSettings
	for _, row := range rows {
		switch proxymemory.Phase(row.Phase) {
		case proxymemory.PhaseExtraction:
			stored.Extraction = row.storedPhase()
		case proxymemory.PhaseConsolidation:
			stored.Consolidation = row.storedPhase()
		}
	}
	return stored, nil
}

// WriteSettings replaces the owner's stored per-phase settings.
//
// One transaction writes both phase rows and their dependency context, so the
// settings never change halfway and an override is never separated from the
// context it was accepted against: a write that fails leaves the stored
// settings exactly as they were (Requirement 2.2). The rows carry the owner's
// own values only; the effective values are recomputed on read and are never
// stored.
func (s *Store) WriteSettings(ctx context.Context, p proxymemory.Principal, value proxymemory.StoredOwnerSettings) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, phase := range []proxymemory.Phase{proxymemory.PhaseExtraction, proxymemory.PhaseConsolidation} {
			row := value.For(phase)
			if _, err := tx.NewRaw(`
				INSERT INTO proxy_memory_settings (
					tenant_id, principal_id, phase, backend, model, reasoning_effort,
					dependency_present, dependency_backend, dependency_model,
					inherited_backend, inherited_model
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (tenant_id, principal_id, phase) DO UPDATE SET
					backend = excluded.backend,
					model = excluded.model,
					reasoning_effort = excluded.reasoning_effort,
					dependency_present = excluded.dependency_present,
					dependency_backend = excluded.dependency_backend,
					dependency_model = excluded.dependency_model,
					inherited_backend = excluded.inherited_backend,
					inherited_model = excluded.inherited_model
			`, p.TenantID, p.PrincipalID, string(phase),
				row.Overrides.Backend, row.Overrides.Model, row.Overrides.ReasoningEffort,
				boolInt(row.Dependency.Present), row.Dependency.Backend, row.Dependency.Model,
				boolInt(row.Dependency.InheritedBackend), boolInt(row.Dependency.InheritedModel)).Exec(ctx); err != nil {
				return opErr("write settings", err)
			}
		}
		return nil
	})
}

// DeleteSettings removes the owner's stored per-phase settings, the trusted
// dependency context included.
//
// One statement removes every phase row of the owner, so the revocation is
// atomic. An owner with no stored settings is ErrNotFound: the caller learns
// that there was nothing to revoke and nothing about any other owner.
func (s *Store) DeleteSettings(ctx context.Context, p proxymemory.Principal) error {
	res, err := s.db.NewRaw(`
		DELETE FROM proxy_memory_settings WHERE tenant_id = ? AND principal_id = ?
	`, p.TenantID, p.PrincipalID).Exec(ctx)
	if err != nil {
		return opErr("delete settings", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return opErr("delete settings rows affected", err)
	}
	if affected == 0 {
		return proxymemory.ErrNotFound
	}
	return nil
}

// boolInt maps a boolean column value onto the integer both engines store and
// return identically.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
