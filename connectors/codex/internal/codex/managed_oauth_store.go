package codex

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

func (s *accountStore) hasUsable() bool {
	_, err := s.pool.Acquire(s.now(), nil)
	return err == nil
}

func (s *accountStore) indexUsable(idx int, now time.Time) bool {
	if idx < 0 || idx >= len(s.meta) {
		return false
	}
	_, err := s.pool.AcquireByID(now, s.meta[idx].poolID)
	return err == nil
}

func (s *accountStore) accountAt(idx int, now time.Time) (managedAccount, error) {
	if idx < 0 || idx >= len(s.meta) {
		return managedAccount{}, fmt.Errorf("%s: invalid account index", ID)
	}
	cred, err := s.pool.AcquireByID(now, s.meta[idx].poolID)
	if err != nil {
		return managedAccount{}, err
	}
	m := s.meta[idx]
	return managedAccount{
		poolID:       m.poolID,
		ID:           m.ID,
		AccessToken:  cred.Secret,
		RefreshToken: m.RefreshToken,
		FilePath:     m.FilePath,
		PlanType:     m.PlanType,
	}, nil
}

func (s *accountStore) selectAccountForSession(session string) (managedAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	session = strings.TrimSpace(session)
	s.evictExpiredAffinityLocked(now)

	if s.strategy == selectionSessionAffinity && session == "" {
		idx, err := s.pickUsableIndexLocked(now, selectionFirstAvailable)
		if err != nil {
			return managedAccount{}, err
		}
		return s.accountAt(idx, now)
	}

	if s.strategy == selectionSessionAffinity {
		if entry, ok := s.affinity[session]; ok {
			if s.affinityFresh(entry, now) && s.indexUsable(entry.accountIdx, now) {
				return s.accountAt(entry.accountIdx, now)
			}
			delete(s.affinity, session)
			s.removeAffinityOrderLocked(session)
		}
		idx, err := s.pickSessionAffinityIndexLocked(session, now)
		if err != nil {
			return managedAccount{}, err
		}
		s.bindAffinityLocked(session, idx, now)
		return s.accountAt(idx, now)
	}

	idx, err := s.pickUsableIndexLocked(now, s.strategy)
	if err != nil {
		return managedAccount{}, err
	}
	return s.accountAt(idx, now)
}

func (s *accountStore) affinityFresh(entry affinityEntry, now time.Time) bool {
	if s.affinityTTL <= 0 {
		return true
	}
	return !entry.boundAt.Add(s.affinityTTL).Before(now)
}

func (s *accountStore) evictExpiredAffinityLocked(now time.Time) {
	if s.affinityTTL <= 0 {
		return
	}
	for session, entry := range s.affinity {
		if !s.affinityFresh(entry, now) {
			delete(s.affinity, session)
			s.removeAffinityOrderLocked(session)
		}
	}
}

func (s *accountStore) bindAffinityLocked(session string, accountIdx int, now time.Time) {
	if _, ok := s.affinity[session]; !ok {
		s.enforceAffinityMaxLocked()
		s.affinityOrder = append(s.affinityOrder, session)
	}
	s.affinity[session] = affinityEntry{accountIdx: accountIdx, boundAt: now}
}

func (s *accountStore) enforceAffinityMaxLocked() {
	if s.affinityMax <= 0 {
		return
	}
	for len(s.affinity) >= s.affinityMax && len(s.affinityOrder) > 0 {
		oldest := s.affinityOrder[0]
		s.affinityOrder = s.affinityOrder[1:]
		delete(s.affinity, oldest)
	}
}

func (s *accountStore) removeAffinityOrderLocked(session string) {
	for i, key := range s.affinityOrder {
		if key == session {
			s.affinityOrder = append(s.affinityOrder[:i], s.affinityOrder[i+1:]...)
			return
		}
	}
}

func (s *accountStore) pickSessionAffinityIndexLocked(session string, now time.Time) (int, error) {
	usable, err := s.usableIndicesLocked(now)
	if err != nil {
		return 0, err
	}
	if len(usable) == 1 {
		return usable[0], nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(session))
	pos := int(h.Sum32() % uint32(len(usable)))
	return usable[pos], nil
}

func (s *accountStore) pickUsableIndexLocked(now time.Time, strategy string) (int, error) {
	usable, err := s.usableIndicesLocked(now)
	if err != nil {
		return 0, err
	}
	switch strategy {
	case selectionRoundRobin:
		pos := s.rrIndex % len(usable)
		s.rrIndex++
		return usable[pos], nil
	default:
		return usable[0], nil
	}
}

func (s *accountStore) usableIndicesLocked(now time.Time) ([]int, error) {
	usable := make([]int, 0, len(s.meta))
	for i := range s.meta {
		if s.indexUsable(i, now) {
			usable = append(usable, i)
		}
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("%s: no usable managed oauth accounts", ID)
	}
	return usable, nil
}

func (s *accountStore) markAuthInvalid(acct managedAccount) {
	s.pool.MarkAuthInvalid(acct.poolID)
}

func (s *accountStore) markRateLimited(acct managedAccount, until time.Time) {
	s.pool.MarkRateLimited(acct.poolID, until)
}
