package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

var ErrTokenExpired = errors.New("token expired")

// ErrMissingCancelParams is returned by parseCancelToken when any required field is absent.
var ErrMissingCancelParams = errors.New("missing required cancel token parameters")

// formatGB formats a byte count as GB to two decimal places (e.g. "4.32 GB").
// Returns "Unknown" for non-positive values.
func formatGB(bytes int64) string {
	if bytes <= 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/float64(int64(1)<<30))
}

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
	// mu guards ttl, which a config reload can change while the reaper
	// goroutine and the request goroutines are running.
	mu      sync.Mutex
	ttl     time.Duration
	ttlWake chan struct{} // buffered(1): tells the reaper its TTL changed
	m       sync.Map
}

func NewStore(ttl time.Duration) *Store {
	return &Store{ttl: ttl, ttlWake: make(chan struct{}, 1)}
}

// SetTTL replaces the token lifetime. New entries expire on the new value and
// the reaper sweeps on it from its next cycle. Entries already registered keep
// the expiry they were given, which is also what their signed token carries.
func (s *Store) SetTTL(ttl time.Duration) {
	s.mu.Lock()
	changed := s.ttl != ttl
	s.ttl = ttl
	s.mu.Unlock()
	if !changed {
		return
	}
	// Buffered and non-blocking: the reaper only needs to know that the TTL
	// moved, so a wake-up already queued covers this change too.
	select {
	case s.ttlWake <- struct{}{}:
	default:
	}
}

// TTL is the token lifetime currently in effect.
func (s *Store) TTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttl
}

func (s *Store) Register(id string, torrentID int64, meta CancelMetadata) {
	s.m.Store(id, &storeEntry{
		torrentID: torrentID,
		expiresAt: time.Now().Add(s.TTL()),
		meta:      meta,
	})
}

// Peek returns the Transmission torrent ID and metadata for the given id without
// consuming the entry. Returns false if the id is not found.
func (s *Store) Peek(id string) (int64, CancelMetadata, bool) {
	val, ok := s.m.Load(id)
	if !ok {
		return 0, CancelMetadata{}, false
	}
	e := val.(*storeEntry)
	return e.torrentID, e.meta, true
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
//
// The goroutine runs even with a non-positive TTL, where it only waits: the
// store is created before the config is known to enable it, so a later
// SetTTL has to be able to start the sweeping.
func (s *Store) StartReaper(ctx context.Context) {
	go func() {
		for {
			ttl := s.TTL()
			if ttl <= 0 {
				// Nothing expires, so wait for a TTL rather than spin.
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
				// The TTL changed: rebuild the ticker on the new interval.
				ticker.Stop()
				continue
			case <-ticker.C:
				ticker.Stop()
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
	if _, err := rand.Read(b); err != nil {
		log.WithError(err).Fatal("crypto/rand unavailable; cannot generate cancel ID")
	}
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

// parseCancelToken parses and validates the id/expiresStr/sig triple extracted from
// URL query params or form values. Returns the parsed Unix expiry on success.
// Returns ErrMissingCancelParams when any field is absent, ErrTokenExpired when
// the signature is valid but the token has elapsed.
func parseCancelToken(secret []byte, id, expiresStr, sig string) (int64, error) {
	if id == "" || expiresStr == "" || sig == "" {
		return 0, ErrMissingCancelParams
	}
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid expires parameter: %w", err)
	}
	return expires, ValidateToken(secret, id, expires, sig)
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
