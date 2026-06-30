package main

import (
	"context"
	"errors"
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

// --- Store ---

func TestStore_RegisterAndTake(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-1", 42)

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
	s.Register("id-1", 42)
	s.Take("id-1")

	_, ok := s.Take("id-1")
	assert.False(t, ok, "second Take should return false after entry consumed")
}

func TestStore_Delete(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-1", 42)
	s.Delete("id-1")

	_, ok := s.Take("id-1")
	assert.False(t, ok)
}

func TestStore_Reaper_RemovesExpired(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	s.Register("id-expire", 99)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(200 * time.Millisecond)

	_, ok := s.Take("id-expire")
	assert.False(t, ok, "expired entry should have been reaped")
}

func TestStore_Reaper_KeepsFreshEntries(t *testing.T) {
	s := NewStore(time.Hour)
	s.Register("id-fresh", 77)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(50 * time.Millisecond)

	_, ok := s.Take("id-fresh")
	assert.True(t, ok, "fresh entry should not be reaped")
}
