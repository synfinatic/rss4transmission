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
	ttl time.Duration
	m   sync.Map
}

func NewStartStore(ttl time.Duration) *StartStore {
	return &StartStore{ttl: ttl}
}

func (s *StartStore) Register(id string, meta StartMetadata) {
	s.m.Store(id, &startEntry{
		expiresAt: time.Now().Add(s.ttl),
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
// elapsed. It stops when ctx is cancelled. It is a no-op if the TTL is non-positive.
func (s *StartStore) StartReaper(ctx context.Context) {
	if s.ttl <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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
