package codex

import (
	"container/list"
	cryptorand "crypto/rand"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	ErrCheckpointClosed      = errors.New("checkpoint store closed")
	ErrCheckpointReservation = errors.New("checkpoint reservation is stale or invalid")
	ErrCheckpointInvalid     = errors.New("checkpoint is invalid")
	ErrCheckpointTooLarge    = errors.New("checkpoint exceeds configured bounds")
)

// CheckpointKey is the complete connector-local authority for a checkpoint.
// SessionID is supplied by the coordinator after proxy session authentication;
// client-only hints must never be used to construct this key.
type CheckpointKey struct {
	ConnectorInstanceID string
	SessionID           string
	AccountID           string
	Model               string
	PromptCacheKey      string
	ClientFamily        string
	CompHash            string
	InstructionsFP      string
	ToolsFP             string
	ContinuityMode      string
}

// NativeUsageEvidence is provider usage evidence retained as metadata only.
// It contains no request or response content.
type NativeUsageEvidence struct {
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
	UsagePresence aipapi.UsagePresence
	Source        aipapi.UsageSource
	Authority     aipapi.UsageAuthority
	DedupeKey     string
}

// NativeCheckpoint is an immutable snapshot once accepted by the store.
// Slices and opaque input items are copied on both ingress and egress.
type NativeCheckpoint struct {
	Key            CheckpointKey
	SourcePrefixFP []string
	Replacement    []inputItem
	CreatedAt      time.Time
	ExpiresAt      time.Time

	SourceEstimatedTokens  int64
	ResultEstimatedTokens  int64
	CompactionInputTokens  int64
	CompactionOutputTokens int64
	SourceUsage            *NativeUsageEvidence
	ResultUsage            *NativeUsageEvidence
	CompactionUsage        *NativeUsageEvidence
}

// Reservation is a single-use capability for one candidate checkpoint.
// Its token is intentionally opaque to callers.
type Reservation struct {
	key   CheckpointKey
	stamp [16]byte
}

type nativeCheckpointEntry struct {
	checkpoint NativeCheckpoint
	bytes      int
	elem       *list.Element
}

type nativeCheckpointStore struct {
	mu            sync.Mutex
	ttl           time.Duration
	maxEntries    int
	maxEntryBytes int
	now           func() time.Time
	entries       map[CheckpointKey]nativeCheckpointEntry
	order         *list.List
	reservations  map[CheckpointKey]Reservation
	cooldowns     map[CheckpointKey]time.Time
	closed        bool
	onEvict       func()
}

func (s *nativeCheckpointStore) setEvictionHook(onEvict func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onEvict = onEvict
	s.mu.Unlock()
}

func (s *nativeCheckpointStore) clockNow() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

type NativeCheckpointStore interface {
	Get(CheckpointKey) (NativeCheckpoint, bool)
	Reserve(CheckpointKey) (Reservation, bool)
	Commit(Reservation, NativeCheckpoint) error
	Abort(Reservation)
	MarkFailure(CheckpointKey, time.Time)
	InCooldown(CheckpointKey) bool
	Invalidate(CheckpointKey)
	Close()
}

func newNativeCheckpointStore(ttl time.Duration, maxEntries, maxEntryBytes int) *nativeCheckpointStore {
	return newNativeCheckpointStoreWithClock(ttl, maxEntries, maxEntryBytes, time.Now)
}

func newNativeCheckpointStoreWithClock(ttl time.Duration, maxEntries, maxEntryBytes int, now func() time.Time) *nativeCheckpointStore {
	if ttl <= 0 {
		ttl = DefaultStateTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxEntryBytes <= 0 {
		maxEntryBytes = DefaultMaxEntryBytes
	}
	if now == nil {
		now = time.Now
	}
	return &nativeCheckpointStore{
		ttl: ttl, maxEntries: maxEntries, maxEntryBytes: maxEntryBytes, now: now,
		entries:      make(map[CheckpointKey]nativeCheckpointEntry),
		order:        list.New(),
		reservations: make(map[CheckpointKey]Reservation),
		cooldowns:    make(map[CheckpointKey]time.Time),
	}
}

func validCheckpointKey(key CheckpointKey) bool {
	values := [...]string{
		key.ConnectorInstanceID, key.SessionID, key.AccountID, key.Model,
		key.PromptCacheKey, key.ClientFamily, key.CompHash, key.InstructionsFP,
		key.ToolsFP, key.ContinuityMode,
	}
	for _, value := range values {
		if !safeCheckpointDimension(value) {
			return false
		}
	}
	return true
}

func validCheckpointAuthority(key CheckpointKey) bool {
	return safeCheckpointDimension(key.ConnectorInstanceID) && safeCheckpointDimension(key.SessionID) &&
		safeCheckpointDimension(key.AccountID) && safeCheckpointDimension(key.Model)
}

func safeCheckpointDimension(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 4096 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *nativeCheckpointStore) Get(key CheckpointKey) (NativeCheckpoint, bool) {
	if s == nil || !validCheckpointKey(key) || !validCheckpointAuthority(key) {
		return NativeCheckpoint{}, false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return NativeCheckpoint{}, false
	}
	evictions := s.purgeExpiredLocked()
	entry, ok := s.entries[key]
	if !ok {
		s.mu.Unlock()
		s.notifyEvictions(evictions)
		return NativeCheckpoint{}, false
	}
	s.touchLocked(key)
	checkpoint := cloneNativeCheckpoint(entry.checkpoint)
	s.mu.Unlock()
	s.notifyEvictions(evictions)
	return checkpoint, true
}

func (s *nativeCheckpointStore) Reserve(key CheckpointKey) (Reservation, bool) {
	if s == nil || !validCheckpointKey(key) || !validCheckpointAuthority(key) {
		return Reservation{}, false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Reservation{}, false
	}
	evictions := s.purgeExpiredLocked()
	if until, ok := s.cooldowns[key]; ok && until.After(s.clockNow()) {
		s.mu.Unlock()
		s.notifyEvictions(evictions)
		return Reservation{}, false
	}
	if _, ok := s.reservations[key]; ok {
		s.mu.Unlock()
		s.notifyEvictions(evictions)
		return Reservation{}, false
	}
	var token [16]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		s.mu.Unlock()
		s.notifyEvictions(evictions)
		return Reservation{}, false
	}
	if token == [16]byte{} {
		s.mu.Unlock()
		s.notifyEvictions(evictions)
		return Reservation{}, false
	}
	reservation := Reservation{key: key, stamp: token}
	s.reservations[key] = reservation
	s.mu.Unlock()
	s.notifyEvictions(evictions)
	return reservation, true
}

func (s *nativeCheckpointStore) Commit(reservation Reservation, checkpoint NativeCheckpoint) error {
	if s == nil {
		return ErrCheckpointClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrCheckpointClosed
	}
	active, ok := s.reservations[reservation.key]
	if !ok || active != reservation || reservation.stamp == [16]byte{} {
		s.mu.Unlock()
		return ErrCheckpointReservation
	}
	if checkpoint.Key != reservation.key || !validCheckpointAuthority(checkpoint.Key) {
		s.mu.Unlock()
		return ErrCheckpointInvalid
	}
	if checkpoint.SourcePrefixFP == nil || checkpoint.Replacement == nil {
		s.mu.Unlock()
		return ErrCheckpointInvalid
	}
	bytes, valid := validateAndMeasureCheckpoint(checkpoint)
	if !valid {
		s.mu.Unlock()
		return ErrCheckpointInvalid
	}
	if bytes > s.maxEntryBytes {
		s.mu.Unlock()
		return ErrCheckpointTooLarge
	}
	checkpoint = cloneNativeCheckpoint(checkpoint)
	now := s.clockNow()
	checkpoint.CreatedAt = now
	checkpoint.ExpiresAt = now.Add(s.ttl)
	if s.order == nil {
		s.order = list.New()
	}
	elem := s.order.PushBack(reservation.key)
	s.entries[reservation.key] = nativeCheckpointEntry{checkpoint: checkpoint, bytes: bytes, elem: elem}
	delete(s.reservations, reservation.key)
	evictions := s.evictOverflowLocked()
	s.mu.Unlock()
	s.notifyEvictions(evictions)
	return nil
}

func (s *nativeCheckpointStore) Abort(reservation Reservation) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if active, ok := s.reservations[reservation.key]; ok && active == reservation {
		delete(s.reservations, reservation.key)
	}
	s.mu.Unlock()
}

func (s *nativeCheckpointStore) MarkFailure(key CheckpointKey, until time.Time) {
	if s == nil || !validCheckpointKey(key) || !validCheckpointAuthority(key) {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if until.After(s.clockNow()) {
		s.cooldowns[key] = until
	} else {
		delete(s.cooldowns, key)
	}
	s.mu.Unlock()
}

func (s *nativeCheckpointStore) InCooldown(key CheckpointKey) bool {
	if s == nil || !validCheckpointKey(key) || !validCheckpointAuthority(key) {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	until, ok := s.cooldowns[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if !until.After(s.clockNow()) {
		delete(s.cooldowns, key)
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	return true
}

func (s *nativeCheckpointStore) Invalidate(key CheckpointKey) {
	if s == nil || !validCheckpointKey(key) {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	removed := s.removeEntryLocked(key)
	delete(s.cooldowns, key)
	s.mu.Unlock()
	if removed {
		s.notifyEvictions(1)
	}
}

func (s *nativeCheckpointStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.entries = make(map[CheckpointKey]nativeCheckpointEntry)
	s.reservations = make(map[CheckpointKey]Reservation)
	s.cooldowns = make(map[CheckpointKey]time.Time)
	if s.order != nil {
		s.order.Init()
	}
}

func (s *nativeCheckpointStore) purgeExpiredLocked() int {
	evictions := 0
	now := s.clockNow()
	for key, entry := range s.entries {
		if !entry.checkpoint.ExpiresAt.After(now) && s.reservations[key].stamp == [16]byte{} {
			if s.removeEntryLocked(key) {
				evictions++
			}
		}
	}
	for key, until := range s.cooldowns {
		if !until.After(now) {
			delete(s.cooldowns, key)
		}
	}
	return evictions
}

func (s *nativeCheckpointStore) evictOverflowLocked() int {
	evictions := 0
	if s.order == nil {
		return evictions
	}
	for len(s.entries) > s.maxEntries && s.order.Len() > 0 {
		var toRemove *list.Element
		for elem := s.order.Front(); elem != nil; elem = elem.Next() {
			key, ok := elem.Value.(CheckpointKey)
			if !ok {
				continue
			}
			if _, reserved := s.reservations[key]; !reserved {
				toRemove = elem
				break
			}
		}
		if toRemove == nil {
			return evictions
		}
		key, ok := toRemove.Value.(CheckpointKey)
		if !ok {
			s.order.Remove(toRemove)
			continue
		}
		if s.removeEntryLocked(key) {
			evictions++
		}
	}
	return evictions
}

func (s *nativeCheckpointStore) touchLocked(key CheckpointKey) {
	if s.order == nil {
		s.order = list.New()
	}
	if entry, ok := s.entries[key]; ok && entry.elem != nil {
		s.order.MoveToBack(entry.elem)
	}
}

func (s *nativeCheckpointStore) removeEntryLocked(key CheckpointKey) bool {
	if entry, ok := s.entries[key]; ok {
		if entry.elem != nil && s.order != nil {
			s.order.Remove(entry.elem)
		}
		delete(s.entries, key)
		return true
	}
	return false
}

func (s *nativeCheckpointStore) notifyEvictions(count int) {
	if count <= 0 {
		return
	}
	s.mu.Lock()
	onEvict := s.onEvict
	s.mu.Unlock()
	if onEvict == nil {
		return
	}
	for range count {
		onEvict()
	}
}

func validStoredCheckpoint(checkpoint NativeCheckpoint) bool {
	_, valid := validateAndMeasureCheckpoint(checkpoint)
	return valid
}

func validateAndMeasureCheckpoint(checkpoint NativeCheckpoint) (int, bool) {
	if !validCheckpointKey(checkpoint.Key) || len(checkpoint.SourcePrefixFP) == 0 || len(checkpoint.Replacement) == 0 {
		return 0, false
	}
	total := 0
	add := func(n int) bool {
		if n < 0 || n > math.MaxInt-total {
			return false
		}
		total += n
		return true
	}
	for _, value := range []string{
		checkpoint.Key.ConnectorInstanceID, checkpoint.Key.SessionID, checkpoint.Key.AccountID,
		checkpoint.Key.Model, checkpoint.Key.PromptCacheKey, checkpoint.Key.ClientFamily,
		checkpoint.Key.CompHash, checkpoint.Key.InstructionsFP, checkpoint.Key.ToolsFP,
		checkpoint.Key.ContinuityMode,
	} {
		if !add(len(value)) {
			return 0, false
		}
	}
	for _, fp := range checkpoint.SourcePrefixFP {
		if !safeCheckpointDimension(fp) || !add(len(fp)) {
			return 0, false
		}
	}
	for _, item := range checkpoint.Replacement {
		if item == nil {
			return 0, false
		}
		body, err := nativeItemJSON(item)
		if err != nil || !add(len(body)) {
			return 0, false
		}
	}
	if err := validateNativeInputPairs(checkpoint.Replacement); err != nil {
		return 0, false
	}
	if checkpoint.SourceEstimatedTokens < 0 || checkpoint.ResultEstimatedTokens < 0 || checkpoint.CompactionInputTokens < 0 || checkpoint.CompactionOutputTokens < 0 {
		return 0, false
	}
	for _, usage := range []*NativeUsageEvidence{checkpoint.SourceUsage, checkpoint.ResultUsage, checkpoint.CompactionUsage} {
		if usage != nil && (usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0) {
			return 0, false
		}
	}
	if !checkpoint.ExpiresAt.IsZero() && checkpoint.ExpiresAt.Before(checkpoint.CreatedAt) {
		return 0, false
	}
	return total, true
}

func cloneNativeCheckpoint(src NativeCheckpoint) NativeCheckpoint {
	dst := src
	dst.SourcePrefixFP = append([]string(nil), src.SourcePrefixFP...)
	dst.Replacement = cloneInputItems(src.Replacement)
	dst.SourceUsage = cloneUsage(src.SourceUsage)
	dst.ResultUsage = cloneUsage(src.ResultUsage)
	dst.CompactionUsage = cloneUsage(src.CompactionUsage)
	return dst
}

func cloneUsage(src *NativeUsageEvidence) *NativeUsageEvidence {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func (r Reservation) String() string {
	if r.stamp == [16]byte{} {
		return "invalid reservation"
	}
	return "opaque reservation"
}
