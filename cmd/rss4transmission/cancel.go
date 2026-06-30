package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrTokenExpired = errors.New("token expired")

// CancelMetadata holds display information about a torrent that is stored
// alongside the Transmission torrent ID in the cancel Store.
type CancelMetadata struct {
	Title     string
	FeedName  string
	Files     []string
	Labels    map[string]string
	SizeBytes int64
}

type storeEntry struct {
	torrentID int64
	expiresAt time.Time
	meta      CancelMetadata
}

// Store maps per-download UUIDs to Transmission torrent IDs.
type Store struct {
	ttl time.Duration
	m   sync.Map
}

func NewStore(ttl time.Duration) *Store {
	return &Store{ttl: ttl}
}

func (s *Store) Register(id string, torrentID int64, meta CancelMetadata) {
	s.m.Store(id, &storeEntry{
		torrentID: torrentID,
		expiresAt: time.Now().Add(s.ttl),
		meta:      meta,
	})
}

// Peek returns the metadata for the given id without consuming the entry.
// Returns false if the id is not found.
func (s *Store) Peek(id string) (CancelMetadata, bool) {
	val, ok := s.m.Load(id)
	if !ok {
		return CancelMetadata{}, false
	}
	return val.(*storeEntry).meta, true
}

// Take removes the entry and returns the Transmission torrent ID. Returns
// false if the id is not found.
func (s *Store) Take(id string) (int64, bool) {
	val, ok := s.m.LoadAndDelete(id)
	if !ok {
		return 0, false
	}
	return val.(*storeEntry).torrentID, true
}

func (s *Store) Delete(id string) {
	s.m.Delete(id)
}

// StartReaper launches a goroutine that removes entries whose token TTL has
// elapsed. It stops when ctx is cancelled.
func (s *Store) StartReaper(ctx context.Context) {
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
					if val.(*storeEntry).expiresAt.Before(now) {
						s.m.Delete(key)
					}
					return true
				})
			}
		}
	}()
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// GenerateToken returns a Unix expiry timestamp and an HMAC-SHA256 signature
// for the given id. The HMAC input is "<id>:<expires>".
func GenerateToken(secret []byte, id string, ttl time.Duration) (int64, string) {
	expires := time.Now().Add(ttl).Unix()
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s:%d", id, expires) //nolint:errcheck,gosec
	return expires, hex.EncodeToString(mac.Sum(nil))
}

// ValidateToken verifies the HMAC signature first, then checks expiry.
// Returns ErrTokenExpired if the signature is valid but the token has expired.
func ValidateToken(secret []byte, id string, expires int64, sig string) error {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s:%d", id, expires) //nolint:errcheck,gosec
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return errors.New("invalid token signature")
	}
	if time.Now().Unix() > expires {
		return ErrTokenExpired
	}
	return nil
}
