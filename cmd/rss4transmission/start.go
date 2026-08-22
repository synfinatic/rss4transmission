package main

import (
	"context"
	"sync"
	"time"
)

// StartMetadata identifies the history record a /start link should resubmit.
type StartMetadata struct {
	FeedName string
	GUID     string
}

type startEntry struct {
	expiresAt time.Time
	meta      StartMetadata
}

// StartStore maps per-notification UUIDs to the feed/GUID pair a /start link
// should resubmit via retryHistoryItem.
type StartStore struct {
	// mu guards ttl, which a config reload can change while the reaper
	// goroutine and the request goroutines are running.
	mu      sync.Mutex
	ttl     time.Duration
	ttlWake chan struct{} // buffered(1): tells the reaper its TTL changed
	m       sync.Map
}

func NewStartStore(ttl time.Duration) *StartStore {
	return &StartStore{ttl: ttl, ttlWake: make(chan struct{}, 1)}
}

// SetTTL replaces the token lifetime. See Store.SetTTL.
func (s *StartStore) SetTTL(ttl time.Duration) {
	s.mu.Lock()
	changed := s.ttl != ttl
	s.ttl = ttl
	s.mu.Unlock()
	if !changed {
		return
	}
	select {
	case s.ttlWake <- struct{}{}:
	default:
	}
}

// TTL is the token lifetime currently in effect.
func (s *StartStore) TTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttl
}

func (s *StartStore) Register(id string, meta StartMetadata) {
	s.m.Store(id, &startEntry{
		expiresAt: time.Now().Add(s.TTL()),
		meta:      meta,
	})
}

// Peek returns the metadata for the given id without consuming the entry.
// Returns false if the id is not found.
func (s *StartStore) Peek(id string) (StartMetadata, bool) {
	val, ok := s.m.Load(id)
	if !ok {
		return StartMetadata{}, false
	}
	return val.(*startEntry).meta, true
}

// StartReaper launches a goroutine that removes entries whose token TTL has
// elapsed. It stops when ctx is cancelled. See Store.StartReaper for why it
// runs even with a non-positive TTL.
func (s *StartStore) StartReaper(ctx context.Context) {
	go func() {
		for {
			ttl := s.TTL()
			if ttl <= 0 {
				select {
				case <-ctx.Done():
					return
				case <-s.ttlWake:
					continue
				}
			}

			ticker := time.NewTicker(ttl)
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-s.ttlWake:
				ticker.Stop()
				continue
			case <-ticker.C:
				ticker.Stop()
				now := time.Now()
				s.m.Range(func(key, val interface{}) bool {
					if val.(*startEntry).expiresAt.Before(now) {
						s.m.Delete(key)
					}
					return true
				})
			}
		}
	}()
}
