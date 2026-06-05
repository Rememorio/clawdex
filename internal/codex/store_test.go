package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivate_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-aaa", "hello world")

	_, err := os.Stat(path)
	require.NoError(t, err, "sessions.json should exist after Activate")

	sessions := store.List(0, 0)
	require.Len(t, sessions, 1)
	assert.Equal(t, int64(1), sessions[0].ChatID)
	assert.Equal(t, "thread-aaa", sessions[0].ThreadID)
	assert.Equal(t, "hello world", sessions[0].Title)
	assert.True(t, sessions[0].Active)
	assert.False(t, sessions[0].UpdatedAt.IsZero())
}

func TestActivate_UpsertByChatAndThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "first prompt")
	store.Activate(1, "thread-1", "updated prompt")

	sessions := store.List(0, 0)
	require.Len(t, sessions, 1, "same chatID+threadID should upsert")
	assert.Equal(t, "thread-1", sessions[0].ThreadID)
	assert.Equal(t, "updated prompt", sessions[0].Title)
}

func TestActivate_DifferentThreadCreatesNewEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "first session")
	time.Sleep(10 * time.Millisecond)
	store.Activate(1, "thread-2", "second session")

	sessions := store.List(0, 0)
	require.Len(t, sessions, 2, "different threadID should create a new entry")
	assert.Equal(t, "thread-2", sessions[0].ThreadID, "newest first")
	assert.Equal(t, "thread-1", sessions[1].ThreadID)
}

func TestActivate_DeactivatesOtherSessionsForSameChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "first")
	assert.Equal(t, "thread-1", store.ActiveSession(1))

	store.Activate(1, "thread-2", "second")
	assert.Equal(t, "thread-2", store.ActiveSession(1))

	// thread-1 should now be inactive.
	sessions := store.List(0, 0)
	for _, s := range sessions {
		if s.ThreadID == "thread-1" {
			assert.False(t, s.Active, "thread-1 should be inactive")
		}
	}
}

func TestActivate_PreservesExistingTitleWhenEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "original title")
	store.Activate(1, "thread-1", "")

	sessions := store.List(0, 0)
	require.Len(t, sessions, 1)
	assert.Equal(t, "original title", sessions[0].Title)
}

func TestDeactivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "session")
	assert.Equal(t, "thread-1", store.ActiveSession(1))

	store.Deactivate(1)
	assert.Equal(t, "", store.ActiveSession(1))

	// Session should still exist in the list, just inactive.
	sessions := store.List(1, 0)
	require.Len(t, sessions, 1)
	assert.False(t, sessions[0].Active)
}

func TestActiveSession_NoActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	assert.Equal(t, "", store.ActiveSession(1))
}

func TestActiveSession_IsolatedByChatID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-a", "chat 1")
	store.Activate(2, "thread-b", "chat 2")

	assert.Equal(t, "thread-a", store.ActiveSession(1))
	assert.Equal(t, "thread-b", store.ActiveSession(2))
}

func TestList_SortedByTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "t-old", "old")
	time.Sleep(10 * time.Millisecond)
	store.Activate(2, "t-new", "new")

	sessions := store.List(0, 0)
	require.Len(t, sessions, 2)
	assert.Equal(t, "t-new", sessions[0].ThreadID, "newest first")
	assert.Equal(t, "t-old", sessions[1].ThreadID)
}

func TestList_Limit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	for i := int64(0); i < 5; i++ {
		store.Activate(i, "t-"+string(rune('a'+i)), "msg")
	}

	sessions := store.List(0, 3)
	assert.Len(t, sessions, 3)
}

func TestList_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	sessions := store.List(0, 10)
	assert.Empty(t, sessions)
}

func TestList_FilterByChatID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-a", "chat 1 session")
	store.Activate(2, "thread-b", "chat 2 session")
	store.Activate(1, "thread-c", "chat 1 session 2")

	// Filter by chat 1.
	sessions := store.List(1, 0)
	require.Len(t, sessions, 2)
	for _, s := range sessions {
		assert.Equal(t, int64(1), s.ChatID)
	}

	// Filter by chat 2.
	sessions = store.List(2, 0)
	require.Len(t, sessions, 1)
	assert.Equal(t, "thread-b", sessions[0].ThreadID)

	// No filter (chatID=0) returns all.
	sessions = store.List(0, 0)
	require.Len(t, sessions, 3)
}

func TestFindByPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "session A")
	store.Activate(2, "019cc76f-aaaa-bbbb-cccc-dddddddddddd", "session B")

	// Global search (chatID=0).
	matches := store.FindByPrefix(0, "019cc781")
	require.Len(t, matches, 1)
	assert.Equal(t, "019cc781-6b9f-7362-ad33-8fc36f7661dd", matches[0].ThreadID)

	// Scoped to chat 1 — finds it.
	matches = store.FindByPrefix(1, "019cc781")
	require.Len(t, matches, 1)

	// Scoped to chat 2 — doesn't find chat 1's session.
	matches = store.FindByPrefix(2, "019cc781")
	assert.Empty(t, matches)
}

func TestFindByPrefix_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "msg")

	matches := store.FindByPrefix(0, "nonexistent")
	assert.Empty(t, matches)
}

func TestNilStore(t *testing.T) {
	var store *SessionStore
	// Should not panic.
	store.Activate(1, "t-1", "msg")
	store.Deactivate(1)
	assert.Equal(t, "", store.ActiveSession(1))
	assert.Nil(t, store.List(0, 10))
	assert.Nil(t, store.FindByPrefix(0, "t"))
}

func TestMemoryCache_LoadsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewSessionStore(path)

	store.Activate(1, "thread-1", "first")

	// Create a second store pointing at the same file — it should load the data.
	store2 := NewSessionStore(path)
	assert.Equal(t, "thread-1", store2.ActiveSession(1))

	// Now modify via store2 and verify store1 still sees its own cache.
	store2.Activate(1, "thread-2", "second")
	// store1 still has thread-1 active in its cache.
	assert.Equal(t, "thread-1", store.ActiveSession(1))
}
