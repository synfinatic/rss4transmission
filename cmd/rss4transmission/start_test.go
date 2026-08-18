package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- StartStore ---

func TestStartStore_RegisterAndPeek(t *testing.T) {
	s := NewStartStore(time.Hour)
	s.Register("id-1", StartMetadata{FeedName: "shows", GUID: "guid-1"})

	meta, ok := s.Peek("id-1")
	require.True(t, ok)
	assert.Equal(t, "shows", meta.FeedName)
	assert.Equal(t, "guid-1", meta.GUID)
}

func TestStartStore_Peek_Missing(t *testing.T) {
	s := NewStartStore(time.Hour)
	_, ok := s.Peek("does-not-exist")
	assert.False(t, ok)
}

func TestStartStore_Peek_DoesNotConsumeEntry(t *testing.T) {
	s := NewStartStore(time.Hour)
	s.Register("id-2", StartMetadata{FeedName: "shows", GUID: "guid-2"})

	_, ok := s.Peek("id-2")
	require.True(t, ok, "first Peek should find the entry")

	_, ok = s.Peek("id-2")
	assert.True(t, ok, "second Peek should still find entry")
}

func TestStartStore_Reaper_RemovesExpired(t *testing.T) {
	s := NewStartStore(50 * time.Millisecond)
	s.Register("id-expire", StartMetadata{FeedName: "shows", GUID: "guid-expire"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(200 * time.Millisecond)

	_, ok := s.Peek("id-expire")
	assert.False(t, ok, "expired entry should have been reaped")
}

func TestStartStore_Reaper_KeepsFreshEntries(t *testing.T) {
	s := NewStartStore(time.Hour)
	s.Register("id-fresh", StartMetadata{FeedName: "shows", GUID: "guid-fresh"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartReaper(ctx)

	time.Sleep(50 * time.Millisecond)

	_, ok := s.Peek("id-fresh")
	assert.True(t, ok, "fresh entry should not be reaped")
}
