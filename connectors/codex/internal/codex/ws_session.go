package codex

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsSessionIdleTTL    = 2 * time.Minute
	wsSessionMaxEntries = 256
)

type wsSessionKey struct {
	baseURL      string
	accountID    string
	accessToken  string
	conversation string
}

type wsSessionStore struct {
	mu         sync.Mutex
	sessions   map[wsSessionKey]*wsSessionConn
	idleTTL    time.Duration
	maxEntries int
	now        func() time.Time
}

type wsSessionConn struct {
	key       wsSessionKey
	store     *wsSessionStore
	sem       chan struct{}
	conn      *websocket.Conn
	lastUsed  time.Time
	idleTimer *time.Timer
}

func newWSSessionStore() *wsSessionStore {
	return &wsSessionStore{
		sessions:   make(map[wsSessionKey]*wsSessionConn),
		idleTTL:    wsSessionIdleTTL,
		maxEntries: wsSessionMaxEntries,
		now:        time.Now,
	}
}

func (s *wsSessionStore) acquire(ctx context.Context, client *http.Client, url string, cfg *Config, convID string) (*wsSessionConn, *http.Response, bool, error) {
	key := wsSessionKey{
		baseURL:      strings.TrimSpace(url),
		accountID:    strings.TrimSpace(cfg.AccountID),
		accessToken:  strings.TrimSpace(cfg.AccessToken),
		conversation: strings.TrimSpace(convID),
	}
	s.mu.Lock()
	session := s.sessions[key]
	if session == nil {
		session = &wsSessionConn{
			key:      key,
			store:    s,
			sem:      make(chan struct{}, 1),
			lastUsed: s.now(),
		}
		s.sessions[key] = session
		s.pruneToCapLocked(session)
	}
	session.stopIdleTimerLocked()
	s.mu.Unlock()

	if err := session.acquire(ctx); err != nil {
		return nil, nil, false, err
	}
	if session.conn != nil {
		return session, nil, true, nil
	}
	conn, resp, err := dialCodexWebSocket(ctx, client, url, cfg, convID)
	if err != nil {
		session.release(true)
		return nil, resp, false, err
	}
	session.conn = conn
	return session, resp, false, nil
}

func (s *wsSessionStore) forgetLocked(key wsSessionKey, session *wsSessionConn) {
	if s.sessions[key] == session {
		delete(s.sessions, key)
	}
}

func (s *wsSessionStore) pruneToCapLocked(protected *wsSessionConn) {
	for len(s.sessions) > s.maxEntries {
		var oldestKey wsSessionKey
		var oldest *wsSessionConn
		for key, session := range s.sessions {
			if session == protected {
				continue
			}
			if !session.tryAcquire() {
				continue
			}
			if oldest == nil || session.lastUsed.Before(oldest.lastUsed) {
				if oldest != nil {
					oldest.unlock()
				}
				oldestKey = key
				oldest = session
				continue
			}
			session.unlock()
		}
		if oldest == nil {
			return
		}
		oldest.closeConnLocked()
		s.forgetLocked(oldestKey, oldest)
		oldest.unlock()
	}
}

func (s *wsSessionStore) closeIdle(key wsSessionKey, session *wsSessionConn) {
	if !session.tryAcquire() {
		return
	}
	defer session.unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	// The idle timer may have fired after acquire already stopped it (Stop returns
	// false for an already-expired timer) and is about to reuse this tracked
	// session. Closing in that case would evict a session an in-flight acquire is
	// reusing and force a redundant dial on an untracked session. Skip only when
	// the session is still the tracked entry AND its timer was stopped since
	// firing. A session that is no longer the map entry (replaced/forgotten) still
	// needs its conn closed to avoid leaking an orphaned connection.
	if s.sessions[key] == session && session.idleTimer == nil {
		return
	}
	session.closeConnLocked()
	session.stopIdleTimerLocked()
	s.forgetLocked(key, session)
}

func (s *wsSessionConn) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *wsSessionConn) tryAcquire() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *wsSessionConn) unlock() {
	select {
	case <-s.sem:
	default:
	}
}

func (s *wsSessionConn) release(closeConn bool) {
	if s.store == nil {
		s.unlock()
		return
	}
	s.store.mu.Lock()
	if closeConn {
		s.closeConnLocked()
		s.stopIdleTimerLocked()
		s.store.forgetLocked(s.key, s)
	} else {
		s.lastUsed = s.store.now()
		s.scheduleIdleTimerLocked()
	}
	s.store.mu.Unlock()
	s.unlock()
}

func (s *wsSessionConn) closeConnLocked() {
	if s.conn == nil {
		return
	}
	_ = s.conn.Close()
	s.conn = nil
}

func (s *wsSessionConn) stopIdleTimerLocked() {
	if s.idleTimer == nil {
		return
	}
	s.idleTimer.Stop()
	s.idleTimer = nil
}

func (s *wsSessionConn) scheduleIdleTimerLocked() {
	s.stopIdleTimerLocked()
	if s.store == nil || s.store.idleTTL <= 0 {
		return
	}
	key := s.key
	store := s.store
	s.idleTimer = time.AfterFunc(s.store.idleTTL, func() {
		store.closeIdle(key, s)
	})
}
