package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- newUUID ---

func TestNewUUID_Format(t *testing.T) {
	id := newUUID()
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	assert.True(t, uuidRe.MatchString(id), "UUID %q does not match expected format", id)
}

func TestNewUUID_Unique(t *testing.T) {
	a := newUUID()
	b := newUUID()
	assert.NotEqual(t, a, b, "two UUIDs should not be equal")
}

// --- GenerateToken / ValidateToken ---

func TestGenerateAndValidateToken_Valid(t *testing.T) {
	secret := []byte("test-secret")
	id := "abc-123"
	ttl := time.Hour

	expires, sig := GenerateToken(secret, id, ttl)
	require.NoError(t, ValidateToken(secret, id, expires, sig))
}

func TestValidateToken_BadSignature(t *testing.T) {
	secret := []byte("test-secret")
	id := "abc-123"
	expires, _ := GenerateToken(secret, id, time.Hour)

	err := ValidateToken(secret, id, expires, "badsig")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenExpired), "bad sig should not be ErrTokenExpired")
}

func TestValidateToken_Expired(t *testing.T) {
	secret := []byte("test-secret")
	id := "abc-123"
	expires, sig := GenerateToken(secret, id, -time.Second) // already expired

	err := ValidateToken(secret, id, expires, sig)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTokenExpired))
}

func TestValidateToken_WrongID(t *testing.T) {
	secret := []byte("test-secret")
	expires, sig := GenerateToken(secret, "original-id", time.Hour)

	err := ValidateToken(secret, "different-id", expires, sig)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenExpired))
}

func TestValidateToken_WrongSecret(t *testing.T) {
	id := "abc-123"
	expires, sig := GenerateToken([]byte("secret-a"), id, time.Hour)

	err := ValidateToken([]byte("secret-b"), id, expires, sig)
	require.Error(t, err)
}

// --- formatGB ---

func TestFormatGB_Positive(t *testing.T) {
	gib := int64(1 << 30)
	assert.Equal(t, "1.00 GB", formatGB(gib))
	assert.Equal(t, "4.32 GB", formatGB(int64(4.32*float64(gib))))
	assert.Equal(t, "0.25 GB", formatGB(gib/4))
}

func TestFormatGB_ZeroOrNegative(t *testing.T) {
	assert.Equal(t, "Unknown", formatGB(0))
	assert.Equal(t, "Unknown", formatGB(-1))
}

// --- parseCancelToken ---

func TestParseCancelToken_Valid(t *testing.T) {
	secret := []byte("secret")
	id := "test-id"
	expires, sig := GenerateToken(secret, id, time.Hour)
	got, err := parseCancelToken(secret, id, fmt.Sprintf("%d", expires), sig)
	require.NoError(t, err)
	assert.Equal(t, expires, got)
}

func TestParseCancelToken_MissingParams(t *testing.T) {
	secret := []byte("secret")
	_, err := parseCancelToken(secret, "", "123", "sig")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingCancelParams))
}

func TestParseCancelToken_Expired(t *testing.T) {
	secret := []byte("secret")
	id := "test-id"
	expires, sig := GenerateToken(secret, id, -time.Second)
	_, err := parseCancelToken(secret, id, fmt.Sprintf("%d", expires), sig)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTokenExpired))
}

func TestParseCancelToken_BadSignature(t *testing.T) {
	secret := []byte("secret")
	id := "test-id"
	expires, _ := GenerateToken(secret, id, time.Hour)
	_, err := parseCancelToken(secret, id, fmt.Sprintf("%d", expires), "badsig")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenExpired))
	assert.False(t, errors.Is(err, ErrMissingCancelParams))
}

// --- Store ---

func TestStore_RegisterAndTake(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-1", 42, CancelMetadata{})

	torrentID, ok := s.Take("id-1")
	assert.True(t, ok)
	assert.Equal(t, int64(42), torrentID)
}

func TestStore_Take_MissingKey(t *testing.T) {
	s := NewStore(time.Hour)

	_, ok := s.Take("nonexistent")
	assert.False(t, ok)
}

func TestStore_Take_RemovesEntry(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-1", 42, CancelMetadata{})
	s.Take("id-1")

	_, ok := s.Take("id-1")
	assert.False(t, ok, "second Take should return false after entry consumed")
}

func TestStore_Delete(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-1", 42, CancelMetadata{})
	s.Delete("id-1")

	_, ok := s.Take("id-1")
	assert.False(t, ok)
}

func TestStore_Reaper_RemovesExpired(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	s.Register("id-expire", 99, CancelMetadata{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(200 * time.Millisecond)

	_, ok := s.Take("id-expire")
	assert.False(t, ok, "expired entry should have been reaped")
}

func TestStore_Reaper_KeepsFreshEntries(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-fresh", 77, CancelMetadata{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(50 * time.Millisecond)

	_, ok := s.Take("id-fresh")
	assert.True(t, ok, "fresh entry should not be reaped")
}

// --- Store.Peek ---

func TestStore_Peek_Found(t *testing.T) {
	s := NewStore(time.Hour)
	meta := CancelMetadata{
		Title:    "My Show S01E01",
		FeedName: "shows",
		Labels:   map[string]string{"show": "My Show", "episode": "S01E01"},
		Files:    []string{"My.Show.S01E01.mkv"},
	}
	s.Register("id-peek", 55, meta)

	torrentID, got, ok := s.Peek("id-peek")
	require.True(t, ok)
	assert.Equal(t, int64(55), torrentID)
	assert.Equal(t, meta.Title, got.Title)
	assert.Equal(t, meta.FeedName, got.FeedName)
	assert.Equal(t, meta.Labels, got.Labels)
	assert.Equal(t, meta.Files, got.Files)
}

func TestStore_Peek_Missing(t *testing.T) {
	s := NewStore(time.Hour)
	_, _, ok := s.Peek("does-not-exist")
	assert.False(t, ok)
}

func TestStore_Peek_DoesNotConsumeEntry(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-2", 88, CancelMetadata{Title: "Keep Me"})

	_, _, ok := s.Peek("id-2")
	require.True(t, ok, "Peek should find the entry")

	// Take should still succeed after Peek.
	torrentID, ok := s.Take("id-2")
	assert.True(t, ok, "Take after Peek should still find entry")
	assert.Equal(t, int64(88), torrentID)
}
