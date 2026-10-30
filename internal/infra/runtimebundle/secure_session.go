package runtimebundle

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/uptrace/bun"
)

type secureSessionRuntime struct {
	manager                                                            *app.Manager
	appStore                                                           app.Store
	recorder                                                           app.GateRecording
	recordingMandatory, requireWorkspaceID, workspaceResolveFailClosed bool
	closer                                                             func() error
}

// secureSessionBuildInput groups dependencies for [buildSecureSessionRuntime].
type secureSessionBuildInput struct {
	StartupContext        context.Context
	Cfg                   *config.Config
	B2B                   b2bua.Store
	Log                   *slog.Logger
	Bundle                *metrics.Bundle
	ControlPlaneStoreWrap func(app.Store) app.Store
	// PostgresPools shares postgres handles via the process registry when non-nil.
	PostgresPools     *db.PoolRegistry
	DualPlaneMigrator *dualPlaneMigrator
}

func buildSecureSessionRuntime(in secureSessionBuildInput) (*secureSessionRuntime, error) {
	startupCtx, cfg, b2b := in.StartupContext, in.Cfg, in.B2B
	log, bundle := in.Log, in.Bundle
	if startupCtx == nil {
		return nil, fmt.Errorf("runtimebundle: nil startup context")
	}
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if !cfg.SecureSessionEffectivelyEnabled() {
		return nil, fmt.Errorf("runtimebundle: secure_session must be enabled (reject explicit enabled: false at config validation)")
	}
	if b2b == nil {
		return nil, fmt.Errorf("runtimebundle: b2bua store is required for secure_session")
	}
	ss := &cfg.SecureSession
	wsOnErr := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError))
	if wsOnErr == "" {
		wsOnErr = "fail_open"
	}
	failClosedWS := wsOnErr == "fail_closed"

	storeName := strings.ToLower(strings.TrimSpace(ss.Store))
	if storeName == "" {
		storeName = "memory"
	}
	key := strings.TrimSpace(ss.TokenFingerprintKey)
	if storeName == "memory" {
		if key == "" {
			buf := make([]byte, 32)
			if _, err := crand.Read(buf); err != nil {
				return nil, fmt.Errorf("runtimebundle: secure_session ephemeral token_fingerprint_key: %w", err)
			}
			key = base64.RawURLEncoding.EncodeToString(buf)
			if log != nil {
				log.InfoContext(startupCtx, "secure_session: memory store token_fingerprint_key omitted; using ephemeral process-local key (resume proofs reset on restart)",
					slog.String("component", "secure_session"), slog.String("store", "memory"), slog.String("notice", "ephemeral_token_fingerprint_key"))
			}
		} else if len(key) < 32 {
			return nil, fmt.Errorf("runtimebundle: secure_session.token_fingerprint_key: when set, must be at least 32 characters (memory store may omit the key for a process-local ephemeral fingerprint)")
		}
	} else if len(key) < 32 {
		return nil, fmt.Errorf("runtimebundle: secure_session requires token_fingerprint_key of at least 32 characters for durable store (sqlite or postgres)")
	}
	fp := []byte(key)
	gen := app.NewRandGenerator(fp)
	lin := b2bualineage.New(b2b)
	if lin == nil {
		return nil, fmt.Errorf("runtimebundle: lineage store is required for secure_session (nil b2bua store)")
	}

	var rw time.Duration
	if s := strings.TrimSpace(ss.ResumeWindow); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session.resume_window: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("runtimebundle: secure_session.resume_window must be positive when set")
		}
		rw = d
	}
	audit := strings.ToLower(strings.TrimSpace(ss.AuditDurability))
	if audit == "" {
		audit = "best_effort"
	}
	requireDurable := audit == "durable"

	var touchCB func(float64)
	if bundle != nil && bundle.SecureSession != nil {
		touchCB = bundle.SecureSession.RecordActivityTouchSeconds
	}

	common := ssAssembleInput{
		wrap: in.ControlPlaneStoreWrap, gen: gen, lin: lin, fp: fp, rw: rw,
		requireDurable: requireDurable, touchCB: touchCB, ss: ss, failClosedWS: failClosedWS,
	}

	switch storeName {
	case "memory":
		if log != nil {
			nd := strings.ToLower(strings.TrimSpace(ss.NonDurableWarning))
			if nd == "" {
				nd = "log"
			}
			if nd == "log" {
				log.InfoContext(startupCtx, "secure_session: using non-durable memory store; session evidence is lost on process restart",
					slog.String("component", "secure_session"), slog.String("store", "memory"), slog.String("notice", "non_durable_store"))
			}
		}
		mem := memory.New(memory.Options{})
		return assembleSecureSession(mem, mem, false, nil, common)
	case "sqlite":
		p := strings.TrimSpace(ss.SQLitePath)
		if p == "" {
			return nil, fmt.Errorf("runtimebundle: secure_session.sqlite_path is required for store sqlite")
		}
		child, cancel := context.WithTimeout(startupCtx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenSQLiteBun(child, p)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: open secure session sqlite: %w", err)
		}
		bunOpts := bunstore.Options{}
		if ttl, maxE, ok := config.EffectiveSecureSessionSQLQueryCache(*ss); ok {
			bunOpts.SQLQueryCacheTTL, bunOpts.SQLQueryCacheMaxEntries = ttl, int(maxE)
		}
		st, err := bunstore.NewWithContextOptions(child, bunDB, bunOpts)
		if err != nil {
			return nil, ssCloseErr(func() error { return bunDB.Close() },
				fmt.Errorf("runtimebundle: secure_session: prepare sqlite schema: %w", err), "sqlite bun db")
		}
		closer := func() error { return st.Close() }
		rt, err := assembleSecureSession(st, st, true, closer, common)
		if err != nil {
			return nil, ssCloseErr(closer, err, "sqlite store")
		}
		return rt, nil
	case "postgres":
		dsn := strings.TrimSpace(ss.PostgresDSN)
		if dsn == "" {
			return nil, fmt.Errorf("runtimebundle: secure_session.postgres_dsn is required for store postgres")
		}
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: %w", err)
		}
		child, cancel := context.WithTimeout(startupCtx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunOpts := bunstore.Options{}
		if ttl, maxE, ok := config.EffectiveSecureSessionSQLQueryCache(*ss); ok {
			bunOpts.SQLQueryCacheTTL, bunOpts.SQLQueryCacheMaxEntries = ttl, int(maxE)
		}
		st, closeFn, err := openPostgresStore(child, dsn, db.PoolSettings{
			MaxOpenConns: poolCfg.MaxOpenConns, MaxIdleConns: poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime, ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}, cfg.Database, in.PostgresPools, in.DualPlaneMigrator, postgresStoreLifecycle[*bunstore.Store]{
			// Migrate/Verify nil: bunstore owns schema preparation on the handle.
			Open: func(ctx context.Context, handle *bun.DB) (*bunstore.Store, error) {
				s, err := bunstore.NewWithContextOptions(ctx, handle, bunOpts)
				if err != nil {
					return nil, fmt.Errorf("runtimebundle: secure_session: prepare postgres schema: %w", err)
				}
				return s, nil
			},
			// Close nil: registry-owned handles are disposed by the registry.
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: secure_session: open postgres store: %w", err)
		}
		closer := closeFn
		rt, err := assembleSecureSession(st, st, true, closer, common)
		if err != nil {
			return nil, ssCloseErr(closer, err, "postgres store")
		}
		return rt, nil
	default:
		return nil, fmt.Errorf("runtimebundle: secure_session.store: want memory, sqlite, or postgres, got %q", ss.Store)
	}
}

type ssAssembleInput struct {
	wrap           func(app.Store) app.Store
	gen            app.Generator
	lin            app.LineageStore
	fp             []byte
	rw             time.Duration
	requireDurable bool
	touchCB        func(float64)
	ss             *config.SecureSessionConfig
	failClosedWS   bool
}

func assembleSecureSession(appStore, delegate app.Store, durable bool, closer func() error, in ssAssembleInput) (*secureSessionRuntime, error) {
	wrapped := wrapSecureSessionStore(delegate, in.wrap)
	rec, err := app.NewRecorder(wrapped)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: secure_session: new recorder: %w", err)
	}
	mgr, err := app.NewManager(wrapped, in.gen, in.lin, app.ManagerConfig{
		ResumeWindow: in.rw, StoreDurable: durable, RequireDurableStore: in.requireDurable,
		FingerprintKey: in.fp, ObserveActivityTouch: in.touchCB,
		ResumeFingerprintPrincipalOnly: in.ss.ResumeTokenBindPrincipalOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: secure_session: new manager: %w", err)
	}
	return &secureSessionRuntime{
		manager: mgr, appStore: appStore, recorder: rec, recordingMandatory: in.requireDurable,
		closer: closer, requireWorkspaceID: in.ss.RequireWorkspaceID, workspaceResolveFailClosed: in.failClosedWS,
	}, nil
}

func ssCloseErr(closer func() error, err error, phase string) error {
	if closer == nil {
		return err
	}
	if cerr := closer(); cerr != nil {
		return errors.Join(err, fmt.Errorf("runtimebundle: close %s after error: %w", phase, cerr))
	}
	return err
}

func securityRuntimeFromSecureSession(ss *secureSessionRuntime) runtime.SecurityRuntime {
	if ss == nil {
		return runtime.SecurityRuntime{}
	}
	return runtime.SecurityRuntime{
		SecureSession: ss.manager, SecureSessionRecorder: ss.recorder,
		SecureSessionRecordingMandatory: ss.recordingMandatory, SessionDenialMapper: aipapidenial.MapToSessionDenial,
		SecureSessionRequireWorkspaceID: ss.requireWorkspaceID, SecureSessionWorkspaceResolveFailClosed: ss.workspaceResolveFailClosed,
	}
}

func wrapSecureSessionStore(delegate app.Store, wrap func(app.Store) app.Store) app.Store {
	if wrap == nil {
		return delegate
	}
	return wrap(delegate)
}
