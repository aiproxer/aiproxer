package dbparity

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BackendClass classifies a persistence capability's backend and topology posture.
type BackendClass string

const (
	Common              BackendClass = "common"
	SQLiteSpecific      BackendClass = "sqlite"
	PostgresDirect      BackendClass = "postgres-direct"
	PostgresDistributed BackendClass = "postgres-distributed"
	PostgresPooler      BackendClass = "postgres-pooler"
)

// ValidBackendClasses returns the recognized backend classes.
func ValidBackendClasses() []BackendClass {
	return []BackendClass{
		Common,
		SQLiteSpecific,
		PostgresDirect,
		PostgresDistributed,
		PostgresPooler,
	}
}

// IsValid reports whether the backend class is recognized.
func (b BackendClass) IsValid() bool {
	return slices.Contains(ValidBackendClasses(), b)
}

// Capability describes an individual persistence capability and its evidence posture.
type Capability struct {
	ID        string       `json:"id"`
	Class     BackendClass `json:"class"`
	Evidence  string       `json:"evidence"`
	Rationale string       `json:"rationale,omitempty"` // Required for non-common capabilities
}

// Component records the authoritative source, test, migration, and contract metadata
// for a production persistence component supporting SQLite and PostgreSQL.
type Component struct {
	ID             string       `json:"id"`
	SourceRoots    []string     `json:"source_roots"`
	TestPackages   []string     `json:"test_packages"`
	StoreContracts []string     `json:"store_contracts"`
	MigrationRoots []string     `json:"migration_roots"`
	Capabilities   []Capability `json:"capabilities"`
}

// Catalog captures all dual-dialect persistence components and shared infrastructure.
type Catalog struct {
	Components  []Component `json:"components"`
	SharedInfra []string    `json:"shared_infra"`
}

// Inventory is a type alias for Catalog to preserve backward compatibility.
type Inventory = Catalog

// DefaultCatalog returns the frozen canonical catalog of all 9 production persistence families
// and shared database infrastructure, audited against current repository state.
//
// The usage-authority family moved to the Enterprise tree with task 6.1 Slice
// A and the billing family with task 6.3; the Enterprise catalog composes this
// catalog with the Enterprise usage-authority and billing components.
func DefaultCatalog() Catalog {
	return Catalog{
		Components: []Component{
			{
				ID: "concurrency-authority",
				SourceRoots: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				TestPackages: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				StoreContracts: []string{
					"internal/core/concurrencyauthority/app.LeaseStore",
				},
				MigrationRoots: []string{
					"internal/infra/concurrencyauthority/leasestore",
				},
				Capabilities: []Capability{
					{
						ID:       "acquire-renew-release-query",
						Class:    Common,
						Evidence: "internal/infra/concurrencyauthority/leasestore/contract_test.go",
					},
					{
						ID:       "atomic-lease-set-operations",
						Class:    Common,
						Evidence: "internal/infra/concurrencyauthority/leasestore/contract_test.go",
					},
					{
						ID:        "postgres-distributed-strictness",
						Class:     PostgresDistributed,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/postgres_integration_test.go",
						Rationale: "PostgreSQL provides multi-instance distributed strict row locks and schema catalog checks.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction poolers require non-session-pinned lease management under dual-endpoint topology.",
					},
					{
						ID:        "sqlite-single-node-serialization",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/concurrencyauthority/leasestore/sqlite_test.go",
						Rationale: "SQLite leases operate in single-node mode using serialized writer transactions.",
					},
				},
			},
			{
				ID: "continuity",
				SourceRoots: []string{
					"internal/core/b2bua",
					"internal/core/continuity/bunstore",
					"internal/infra/conversationview",
				},
				TestPackages: []string{
					"internal/core/continuity/bunstore",
					"internal/infra/conversationview",
				},
				StoreContracts: []string{
					"internal/core/b2bua.ALegRetirementObserver",
					"internal/core/b2bua.BLegAllocationAuthority",
					"internal/core/b2bua.InterleavedStateStore",
					"internal/core/b2bua.Store",
					"internal/infra/conversationview.Store",
					"internal/infra/conversationview.BootstrapStore",
					"internal/core/routeoverride.Store",
				},
				MigrationRoots: []string{
					"internal/core/continuity/bunstore",
				},
				Capabilities: []Capability{
					{
						ID:       "a-leg-lifecycle-lineage-persistence",
						Class:    Common,
						Evidence: "internal/core/continuity/bunstore/store_test.go",
					},
					{
						ID:       "conversation-view-snapshot-mutations",
						Class:    Common,
						Evidence: "internal/infra/conversationview/bun_store_test.go",
					},
					{
						ID:       "conversation-view-atomic-bootstrap",
						Class:    Common,
						Evidence: "internal/infra/conversationview/bootstrap_durable_test.go",
					},
					{
						ID:       "route-override-persistence",
						Class:    Common,
						Evidence: "internal/core/continuity/bunstore/routeoverride_persist_test.go",
					},
					{
						ID:        "postgres-direct-b-leg-returning-allocation",
						Class:     PostgresDirect,
						Evidence:  "internal/core/continuity/bunstore/postgres_integration_test.go",
						Rationale: "PostgreSQL uses atomic UPDATE ... RETURNING for B-leg sequence allocation.",
					},
					{
						ID:        "sqlite-serialized-b-leg-allocation",
						Class:     SQLiteSpecific,
						Evidence:  "internal/core/continuity/bunstore/legacy_sqlite_compat_test.go",
						Rationale: "SQLite relies on serialized write transactions for B-leg allocation.",
					},
				},
			},
			{
				ID: "control-plane-ledger",
				SourceRoots: []string{
					"internal/infra/controlplane/ledgerstore",
				},
				TestPackages: []string{
					"internal/infra/controlplane/ledgerstore",
				},
				StoreContracts: []string{
					"internal/core/controlplane.Store",
				},
				MigrationRoots: []string{
					"internal/infra/controlplane/ledgerstore",
				},
				Capabilities: []Capability{
					{
						ID:       "append-query-projections-retention",
						Class:    Common,
						Evidence: "internal/infra/controlplane/ledgerstore/contract/contract.go",
					},
					{
						ID:        "postgres-direct-catalog-schema",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/controlplane/ledgerstore/postgres_integration_test.go",
						Rationale: "PostgreSQL validates information_schema catalog constraints and type bindings.",
					},
					{
						ID:        "sqlite-local-type-adaptation",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/controlplane/ledgerstore/store_test.go",
						Rationale: "SQLite adapts boolean and integer placeholders via driver-specific query translation.",
					},
				},
			},
			{
				ID: "metering-journal",
				SourceRoots: []string{
					"internal/infra/metering/journalstore",
				},
				TestPackages: []string{
					"internal/infra/metering/journalstore",
				},
				StoreContracts: []string{
					"pkg/aipsdk/metering.Querier",
					"pkg/aipsdk/metering.Recorder",
					"internal/core/metering.AccountWindowStore",
				},
				MigrationRoots: []string{
					"internal/infra/metering/journalstore",
				},
				Capabilities: []Capability{
					{
						ID:       "fact-append-query-restart-reconstruction",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase3_contract_red_test.go",
					},
					{
						ID:       "schema-v2-bounded-queries",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase35_bounded_query_test.go",
					},
					{
						ID:        "postgres-direct-schema-verification",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/metering/journalstore/postgres_integration_test.go",
						Rationale: "PostgreSQL verifies table structures, bounded index definitions, and migration history.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/metering/journalstore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction pooler support for metering fact intake.",
					},
					{
						ID:        "sqlite-busy-retry-backoff",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/metering/journalstore/sqlite_retry_contract_test.go",
						Rationale: "SQLite requires locked/busy error classification and backoff under concurrency.",
					},
					{
						ID:       "v2-observation-components-replay-rebuild",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase4_observation_store_test.go",
					},
					{
						ID:       "account-window-gauge-history-current-asof",
						Class:    Common,
						Evidence: "internal/infra/metering/journalstore/phase11_account_window_test.go",
					},
				},
			},
			{
				ID: "proxy-memory",
				SourceRoots: []string{
					"internal/infra/proxymemorystore",
				},
				TestPackages: []string{
					"internal/infra/proxymemorystore",
				},
				StoreContracts: []string{
					"internal/plugins/features/proxymemory.Store",
				},
				MigrationRoots: []string{
					"internal/infra/proxymemorystore",
				},
				Capabilities: []Capability{
					{
						ID:       "owner-isolated-queries",
						Class:    Common,
						Evidence: "internal/infra/proxymemorystore/contract_test.go",
					},
					{
						ID:       "lease-claim-at-most-one",
						Class:    Common,
						Evidence: "internal/infra/proxymemorystore/contract_test.go",
					},
					{
						ID:       "cas-publish",
						Class:    Common,
						Evidence: "internal/infra/proxymemorystore/contract_test.go",
					},
					{
						ID:       "forget-fence",
						Class:    Common,
						Evidence: "internal/infra/proxymemorystore/contract_test.go",
					},
					{
						// The owner-scoped settings of design §4: the owner's
						// own unprojected per-phase overrides, keyed by tenant
						// and principal without a project, in the isolated
						// feature persistence.
						ID:       "owner-scoped-settings-overrides",
						Class:    Common,
						Evidence: "internal/infra/proxymemorystore/contract_test.go",
					},
				},
			},
			{
				ID: "secure-sessions",
				SourceRoots: []string{
					"internal/core/securesession/adapters/bunstore",
				},
				TestPackages: []string{
					"internal/core/securesession/adapters/bunstore",
					"internal/core/securesession/storecontract",
				},
				StoreContracts: []string{
					"internal/core/securesession/app.SessionUsageRollup",
					"internal/core/securesession/app.Store",
				},
				MigrationRoots: []string{
					"internal/core/securesession/adapters/bunstore",
				},
				Capabilities: []Capability{
					{
						ID:       "session-lifecycle-transcript-audit-quarantine",
						Class:    Common,
						Evidence: "internal/core/securesession/storecontract/contract.go",
					},
					{
						ID:        "postgres-direct-text-json-bytea-handling",
						Class:     PostgresDirect,
						Evidence:  "internal/core/securesession/storecontract/postgres_bun_contract_test.go",
						Rationale: "PostgreSQL requires JSON text binding to prevent BYTEA hex-escaping on readback.",
					},
					{
						ID:        "sqlite-pragma-table-info-migration",
						Class:     SQLiteSpecific,
						Evidence:  "internal/core/securesession/adapters/bunstore/legacy_sqlite_compat_test.go",
						Rationale: "SQLite uses pragma_table_info column probes for safe incremental schema migration.",
					},
				},
			},
			{
				ID: "session-classification",
				SourceRoots: []string{
					"internal/standardplugins/featurehost/sessionclassification",
				},
				TestPackages: []string{
					"internal/standardplugins/featurehost/sessionclassification",
				},
				StoreContracts: []string{
					"internal/plugins/features/sessionclassification.Store",
				},
				MigrationRoots: []string{
					"internal/standardplugins/featurehost/sessionclassification",
				},
				Capabilities: []Capability{
					{
						ID:       "bounded-load-promote-schema-restart-parity",
						Class:    Common,
						Evidence: "internal/standardplugins/featurehost/sessionclassification/dbparity_test.go:runSessionClassificationParityContract",
					},
				},
			},
			{
				ID: "terminal-work",
				SourceRoots: []string{
					"internal/infra/terminalwork/workstore",
				},
				TestPackages: []string{
					"internal/infra/terminalwork/workstore",
				},
				StoreContracts: []string{
					"internal/core/terminalwork/app.RecoveryStore",
					"internal/core/terminalwork/app.WorkStore",
				},
				MigrationRoots: []string{
					"internal/infra/terminalwork/workstore",
				},
				Capabilities: []Capability{
					{
						ID:       "claim-renew-complete-retry-quarantine",
						Class:    Common,
						Evidence: "internal/infra/terminalwork/workstore/phase43_contract_test.go",
					},
					{
						ID:       "runtime-generation-and-instance-pinning",
						Class:    Common,
						Evidence: "internal/infra/terminalwork/workstore/runtime_generation_migration_test.go",
					},
					{
						ID:        "postgres-direct-integration",
						Class:     PostgresDirect,
						Evidence:  "internal/infra/terminalwork/workstore/postgres_integration_test.go",
						Rationale: "PostgreSQL verifies schema migration history, unique constraint violations, and processor execution.",
					},
					{
						ID:        "postgres-pooler-support",
						Class:     PostgresPooler,
						Evidence:  "internal/infra/terminalwork/workstore/postgres_pooled_integration_test.go",
						Rationale: "PostgreSQL transaction pooler support for work claim/heartbeat loop.",
					},
					{
						ID:        "sqlite-local-execution",
						Class:     SQLiteSpecific,
						Evidence:  "internal/infra/terminalwork/workstore/append_outcome_test.go",
						Rationale: "SQLite executes durable work claims and queries against local storage.",
					},
				},
			},
		},
		SharedInfra: []string{
			"internal/bunmigrate",
			"internal/infra/db",
			"internal/infra/dbmigrate",
			"internal/infra/runtimebundle",
		},
	}
}

// DefaultInventory returns the inventory for backward compatibility.
func DefaultInventory() Inventory {
	return DefaultCatalog()
}

// Validate verifies that the catalog satisfies all structural invariants:
// - no empty component list
// - no duplicate or blank component IDs
// - deterministic alphabetical sorting of components by ID
// - non-empty source roots, test packages, migration roots, store contracts, capabilities
// - no duplicate or blank paths/contracts within components
// - no duplicate source root or migration root ownership across components
// - shared infrastructure roots are disjoint from component source/migration roots
// - non-common capabilities have non-empty rationale and non-empty evidence
// - common capabilities have non-empty evidence
// - all capability classes are valid
// - at least one Common capability per component
func (c Catalog) Validate() error {
	if len(c.Components) == 0 {
		return fmt.Errorf("catalog contains no components")
	}

	sharedMap := make(map[string]struct{}, len(c.SharedInfra))
	for _, infra := range c.SharedInfra {
		if strings.TrimSpace(infra) == "" {
			return fmt.Errorf("shared infrastructure entry must not be blank")
		}
		if _, exists := sharedMap[infra]; exists {
			return fmt.Errorf("duplicate shared infrastructure entry %q", infra)
		}
		sharedMap[infra] = struct{}{}
	}

	seenComponentIDs := make(map[string]struct{}, len(c.Components))
	seenSourceRoots := make(map[string]string)
	seenMigrationRoots := make(map[string]string)

	var prevID string
	for i, comp := range c.Components {
		if strings.TrimSpace(comp.ID) == "" {
			return fmt.Errorf("component at index %d has blank ID", i)
		}
		if _, ok := seenComponentIDs[comp.ID]; ok {
			return fmt.Errorf("duplicate component ID %q", comp.ID)
		}
		seenComponentIDs[comp.ID] = struct{}{}

		if prevID != "" && comp.ID <= prevID {
			return fmt.Errorf("components are not sorted deterministically: %q appears after %q", comp.ID, prevID)
		}
		prevID = comp.ID

		if len(comp.SourceRoots) == 0 {
			return fmt.Errorf("component %q has no source roots", comp.ID)
		}
		seenCompSrcRoots := make(map[string]struct{}, len(comp.SourceRoots))
		for _, src := range comp.SourceRoots {
			if strings.TrimSpace(src) == "" {
				return fmt.Errorf("component %q has blank source root", comp.ID)
			}
			if _, exists := seenCompSrcRoots[src]; exists {
				return fmt.Errorf("component %q has duplicate source root %q", comp.ID, src)
			}
			seenCompSrcRoots[src] = struct{}{}

			if _, isShared := sharedMap[src]; isShared {
				return fmt.Errorf("component %q source root %q conflicts with shared infrastructure", comp.ID, src)
			}
			if owner, exists := seenSourceRoots[src]; exists {
				return fmt.Errorf("duplicate source root ownership: %q is owned by %q and %q", src, owner, comp.ID)
			}
			seenSourceRoots[src] = comp.ID
		}

		if len(comp.TestPackages) == 0 {
			return fmt.Errorf("component %q has no test packages", comp.ID)
		}
		seenCompTestPkgs := make(map[string]struct{}, len(comp.TestPackages))
		for _, tp := range comp.TestPackages {
			if strings.TrimSpace(tp) == "" {
				return fmt.Errorf("component %q has blank test package", comp.ID)
			}
			if _, exists := seenCompTestPkgs[tp]; exists {
				return fmt.Errorf("component %q has duplicate test package %q", comp.ID, tp)
			}
			seenCompTestPkgs[tp] = struct{}{}
		}

		if len(comp.StoreContracts) == 0 {
			return fmt.Errorf("component %q has no store contracts", comp.ID)
		}
		seenCompStoreContracts := make(map[string]struct{}, len(comp.StoreContracts))
		for _, sc := range comp.StoreContracts {
			if strings.TrimSpace(sc) == "" {
				return fmt.Errorf("component %q has blank store contract", comp.ID)
			}
			if _, exists := seenCompStoreContracts[sc]; exists {
				return fmt.Errorf("component %q has duplicate store contract %q", comp.ID, sc)
			}
			seenCompStoreContracts[sc] = struct{}{}
		}

		if len(comp.MigrationRoots) == 0 {
			return fmt.Errorf("component %q has no migration roots", comp.ID)
		}
		seenCompMigRoots := make(map[string]struct{}, len(comp.MigrationRoots))
		for _, mr := range comp.MigrationRoots {
			if strings.TrimSpace(mr) == "" {
				return fmt.Errorf("component %q has blank migration root", comp.ID)
			}
			if _, exists := seenCompMigRoots[mr]; exists {
				return fmt.Errorf("component %q has duplicate migration root %q", comp.ID, mr)
			}
			seenCompMigRoots[mr] = struct{}{}

			if _, isShared := sharedMap[mr]; isShared {
				return fmt.Errorf("component %q migration root %q conflicts with shared infrastructure", comp.ID, mr)
			}
			if owner, exists := seenMigrationRoots[mr]; exists {
				return fmt.Errorf("duplicate migration root ownership: %q is owned by %q and %q", mr, owner, comp.ID)
			}
			seenMigrationRoots[mr] = comp.ID
		}

		if len(comp.Capabilities) == 0 {
			return fmt.Errorf("component %q has no capabilities", comp.ID)
		}
		capIDs := make(map[string]struct{}, len(comp.Capabilities))
		hasCommon := false
		for _, cap := range comp.Capabilities {
			if strings.TrimSpace(cap.ID) == "" {
				return fmt.Errorf("component %q has capability with blank ID", comp.ID)
			}
			if _, ok := capIDs[cap.ID]; ok {
				return fmt.Errorf("component %q has duplicate capability ID %q", comp.ID, cap.ID)
			}
			capIDs[cap.ID] = struct{}{}

			if !cap.Class.IsValid() {
				return fmt.Errorf("component %q capability %q has invalid BackendClass %q", comp.ID, cap.ID, cap.Class)
			}

			if strings.TrimSpace(cap.Evidence) == "" {
				return fmt.Errorf("component %q capability %q (class %s) has blank evidence", comp.ID, cap.ID, cap.Class)
			}
			if cap.Class == Common {
				hasCommon = true
			} else {
				if strings.TrimSpace(cap.Rationale) == "" {
					return fmt.Errorf("component %q capability %q (%s) has blank rationale", comp.ID, cap.ID, cap.Class)
				}
			}
		}
		if !hasCommon {
			return fmt.Errorf("component %q has no Common capabilities", comp.ID)
		}
	}

	return nil
}

// ValidatePaths verifies that all referenced paths (source roots, test packages,
// migration roots, shared infra, and capability evidence anchors) exist on the filesystem.
func (c Catalog) ValidatePaths(repoRoot string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	for _, comp := range c.Components {
		for _, src := range comp.SourceRoots {
			abs := filepath.Join(repoRoot, filepath.FromSlash(src))
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("component %q source root %q does not exist as directory: %s", comp.ID, src, abs)
			}
		}
		for _, tp := range comp.TestPackages {
			abs := filepath.Join(repoRoot, filepath.FromSlash(tp))
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("component %q test package %q does not exist as directory: %s", comp.ID, tp, abs)
			}
		}
		for _, mr := range comp.MigrationRoots {
			abs := filepath.Join(repoRoot, filepath.FromSlash(mr))
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("component %q migration root %q does not exist as directory: %s", comp.ID, mr, abs)
			}
		}
		for _, cap := range comp.Capabilities {
			evidenceFile := cap.Evidence
			if idx := strings.Index(evidenceFile, ":"); idx >= 0 {
				evidenceFile = evidenceFile[:idx]
			}
			abs := filepath.Join(repoRoot, filepath.FromSlash(evidenceFile))
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("component %q capability %q evidence anchor file %q does not exist: %s", comp.ID, cap.ID, evidenceFile, abs)
			}
		}
	}

	for _, infra := range c.SharedInfra {
		abs := filepath.Join(repoRoot, filepath.FromSlash(infra))
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("shared infrastructure root %q does not exist as directory: %s", infra, abs)
		}
	}

	return nil
}

// ComponentByID returns the component with the given ID, or false if not found.
func (c Catalog) ComponentByID(id string) (Component, bool) {
	idx := slices.IndexFunc(c.Components, func(comp Component) bool {
		return comp.ID == id
	})
	if idx < 0 {
		return Component{}, false
	}
	return c.Components[idx], true
}

// ComponentIDs returns the ordered list of component IDs in the catalog.
func (c Catalog) ComponentIDs() []string {
	ids := make([]string, len(c.Components))
	for i, comp := range c.Components {
		ids[i] = comp.ID
	}
	return ids
}

// AllSourceRoots returns the unique, deterministically sorted list of all source roots.
func (c Catalog) AllSourceRoots() []string {
	seen := make(map[string]struct{})
	var roots []string
	for _, comp := range c.Components {
		for _, src := range comp.SourceRoots {
			if _, ok := seen[src]; !ok {
				seen[src] = struct{}{}
				roots = append(roots, src)
			}
		}
	}
	slices.Sort(roots)
	return roots
}

// AllTestPackages returns the unique, deterministically sorted list of all test packages.
func (c Catalog) AllTestPackages() []string {
	seen := make(map[string]struct{})
	var pkgs []string
	for _, comp := range c.Components {
		for _, tp := range comp.TestPackages {
			if _, ok := seen[tp]; !ok {
				seen[tp] = struct{}{}
				pkgs = append(pkgs, tp)
			}
		}
	}
	slices.Sort(pkgs)
	return pkgs
}

// AllMigrationRoots returns the unique, deterministically sorted list of all migration roots.
func (c Catalog) AllMigrationRoots() []string {
	seen := make(map[string]struct{})
	var roots []string
	for _, comp := range c.Components {
		for _, mr := range comp.MigrationRoots {
			if _, ok := seen[mr]; !ok {
				seen[mr] = struct{}{}
				roots = append(roots, mr)
			}
		}
	}
	slices.Sort(roots)
	return roots
}

// CommonCapabilities returns the list of Common capabilities for the given component ID.
func (c Catalog) CommonCapabilities(componentID string) []Capability {
	comp, ok := c.ComponentByID(componentID)
	if !ok {
		return nil
	}
	var caps []Capability
	for _, cap := range comp.Capabilities {
		if cap.Class == Common {
			caps = append(caps, cap)
		}
	}
	return caps
}

// NonCommonCapabilities returns the list of non-Common capabilities for the given component ID.
func (c Catalog) NonCommonCapabilities(componentID string) []Capability {
	comp, ok := c.ComponentByID(componentID)
	if !ok {
		return nil
	}
	var caps []Capability
	for _, cap := range comp.Capabilities {
		if cap.Class != Common {
			caps = append(caps, cap)
		}
	}
	return caps
}
