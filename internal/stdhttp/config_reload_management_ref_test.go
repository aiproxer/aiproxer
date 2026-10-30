package stdhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Task 1.5 management reload/status reference (req 1.7, 11.4-11.6, 12.x). Not production.

const (
	ConfigReloadPath = "/admin/config/reload"
	ConfigStatusPath = "/admin/config/status"

	ReloadCategoryPublished         = "published"
	ReloadCategoryNoop              = "no-op"
	ReloadCategoryBusy              = "busy"
	ReloadCategoryRestartRequired   = "restart-required"
	ReloadCategoryRetentionBlocked  = "retention-blocked"
	ReloadCategoryInvalid           = "invalid"
	ReloadCategorySourceIntegrity   = "source-integrity-failed"
	ReloadCategoryCanceled          = "canceled"
	ReloadCategoryPreparationFailed = "preparation-failed"
	ReloadCategoryInternalFailed    = "internal-failed"
)

type ReloadTriggerKind string

const (
	ReloadTriggerSIGHUP ReloadTriggerKind = "sighup"
	ReloadTriggerAPI    ReloadTriggerKind = "api"
)

type ReloadTrigger struct {
	Kind       ReloadTriggerKind
	AcceptedAt time.Time
	SafeActor  string
}

type ReloadResult struct {
	Category           string   `json:"category"`
	AttemptID          int64    `json:"attempt_id"`
	ActiveGeneration   int64    `json:"active_generation"`
	PreviousGeneration int64    `json:"previous_generation,omitempty"`
	RestartFields      []string `json:"restart_required_fields,omitempty"`
	RestartFieldCount  int      `json:"restart_required_field_count,omitempty"`
	ReasonCategory     string   `json:"reason_category,omitempty"`
	CoalescedSignals   int64    `json:"coalesced_signals,omitempty"`
}

type ReloadStatus struct {
	ActiveGeneration int64        `json:"active_generation"`
	LastResult       ReloadResult `json:"last_result"`
	Busy             bool         `json:"busy"`
	FixedSourcePath  string       `json:"fixed_source_path"`
}

type RefReloadCoordinator struct {
	mu             sync.Mutex
	busy           bool
	shutdown       atomic.Bool
	attempts       atomic.Int64
	activeGen      atomic.Int64
	last           ReloadResult
	fixedSource    string
	hostTimeout    time.Duration
	reloadFn       func(ctx context.Context, trigger ReloadTrigger) ReloadResult
	acceptedHosted atomic.Int64
	onComplete     func(ReloadResult)
}

func NewRefReloadCoordinator(fixedSource string, fn func(ctx context.Context, trigger ReloadTrigger) ReloadResult) *RefReloadCoordinator {
	c := &RefReloadCoordinator{fixedSource: fixedSource, hostTimeout: time.Minute, reloadFn: fn}
	c.activeGen.Store(1)
	c.last = ReloadResult{Category: ReloadCategoryPublished, ActiveGeneration: 1}
	return c
}

func (c *RefReloadCoordinator) FixedSourcePath() string { return c.fixedSource }
func (c *RefReloadCoordinator) MarkShutdown()           { c.shutdown.Store(true) }
func (c *RefReloadCoordinator) SetOnComplete(fn func(ReloadResult)) {
	c.mu.Lock()
	c.onComplete = fn
	c.mu.Unlock()
}

func (c *RefReloadCoordinator) Status() ReloadStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ReloadStatus{ActiveGeneration: c.activeGen.Load(), LastResult: c.last, Busy: c.busy, FixedSourcePath: c.fixedSource}
}

func (c *RefReloadCoordinator) Reload(ctx context.Context, trigger ReloadTrigger) ReloadResult {
	if c.shutdown.Load() {
		return ReloadResult{Category: ReloadCategoryCanceled, ReasonCategory: "shutdown"}
	}
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return ReloadResult{Category: ReloadCategoryBusy, ActiveGeneration: c.activeGen.Load(), ReasonCategory: "reload-in-progress"}
	}
	c.busy = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.busy = false; c.mu.Unlock() }()

	attempt := c.attempts.Add(1)
	hostCtx := context.WithoutCancel(ctx)
	if c.hostTimeout > 0 {
		var cancel context.CancelFunc
		hostCtx, cancel = context.WithTimeout(hostCtx, c.hostTimeout)
		defer cancel()
	}
	var res ReloadResult
	if c.reloadFn != nil {
		res = c.reloadFn(hostCtx, trigger)
	} else {
		res = ReloadResult{Category: ReloadCategoryPublished}
	}
	res.AttemptID = attempt
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGen.Load()
	}
	if res.Category == ReloadCategoryPublished {
		prev := c.activeGen.Load()
		res.PreviousGeneration = prev
		c.activeGen.Store(prev + 1)
		res.ActiveGeneration = c.activeGen.Load()
	}
	c.mu.Lock()
	c.last = res
	onComplete := c.onComplete
	c.mu.Unlock()
	c.acceptedHosted.Add(1)
	if onComplete != nil {
		onComplete(res)
	}
	return res
}

type ManagementAuthMode int

const (
	ManagementAuthNone ManagementAuthMode = iota
	ManagementAuthBearer
)

type RefConfigReloadManagement struct {
	Coord        *RefReloadCoordinator
	AuthMode     ManagementAuthMode
	BearerToken  string
	AllowOrigins map[string]struct{}
	MaxBodyBytes int64
}

func NewRefConfigReloadManagement(coord *RefReloadCoordinator) *RefConfigReloadManagement {
	return &RefConfigReloadManagement{Coord: coord, AuthMode: ManagementAuthBearer, MaxBodyBytes: 64, AllowOrigins: map[string]struct{}{}}
}

func (h *RefConfigReloadManagement) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(ConfigReloadPath, h.handleReload)
	mux.HandleFunc(ConfigStatusPath, h.handleStatus)
	return mux
}

func (h *RefConfigReloadManagement) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r) || !h.browserGuard(w, r, true) {
		return
	}
	writeJSON(w, http.StatusOK, h.Coord.Status())
}

func (h *RefConfigReloadManagement) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusForbidden) // preflight never authorizes (req 12.7)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r) || !h.browserGuard(w, r, false) {
		return
	}
	if err := h.validateBody(r); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, ReloadResult{Category: ReloadCategoryInvalid, ReasonCategory: err.Error()})
		return
	}
	_ = h.Coord.FixedSourcePath() // fixed-source only (req 1.7, 12.4)
	res := h.Coord.Reload(r.Context(), ReloadTrigger{Kind: ReloadTriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "management-api"})
	writeJSON(w, httpStatusForReload(res.Category), res)
}

func (h *RefConfigReloadManagement) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.AuthMode == ManagementAuthNone {
		return true
	}
	if h.BearerToken == "" || r.Header.Get("Authorization") != "Bearer "+h.BearerToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"category":"unauthorized"}`))
		return false
	}
	return true
}

func (h *RefConfigReloadManagement) browserGuard(w http.ResponseWriter, r *http.Request, statusRead bool) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, ok := h.AllowOrigins[origin]; !ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"category":"browser-origin-rejected"}`))
			return false
		}
	}
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "cross-site", "same-site":
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"category":"fetch-metadata-rejected"}`))
		return false
	case "same-origin":
		if r.Header.Get("Origin") == "" {
			w.WriteHeader(http.StatusForbidden)
			return false
		}
	case "none":
		if !statusRead {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"category":"fetch-metadata-rejected"}`))
			return false
		}
	}
	w.Header().Del("Access-Control-Allow-Origin")
	return true
}

func (h *RefConfigReloadManagement) validateBody(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	limit := h.MaxBodyBytes
	if limit <= 0 {
		limit = 64
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return errors.New("body-read-failed")
	}
	if int64(len(body)) > limit {
		return errors.New("body-too-large")
	}
	if len(body) == 0 {
		return nil
	}
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return errors.New("wrong-content-type")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return errors.New("invalid-json")
	}
	if len(obj) == 0 {
		return nil
	}
	for _, k := range []string{"path", "config", "yaml", "url", "source", "command"} {
		if _, ok := obj[k]; ok {
			return errors.New("source-override-forbidden")
		}
	}
	return errors.New("non-empty-body")
}

func httpStatusForReload(category string) int {
	switch category {
	case ReloadCategoryPublished, ReloadCategoryNoop:
		return http.StatusOK
	case ReloadCategoryBusy, ReloadCategoryRestartRequired, ReloadCategoryRetentionBlocked:
		return http.StatusConflict
	case ReloadCategoryInvalid, ReloadCategorySourceIntegrity:
		return http.StatusUnprocessableEntity
	case ReloadCategoryCanceled, ReloadCategoryPreparationFailed, ReloadCategoryInternalFailed:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
