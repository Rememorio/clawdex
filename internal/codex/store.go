package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// StoredSession represents a single clawdex-managed session entry.
type StoredSession struct {
	ChatID    int64     `json:"chat_id"`
	ThreadID  string    `json:"thread_id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Active    bool      `json:"active"`
}

// SessionStore persists session metadata to a JSON file and keeps an
// in-memory cache so repeated reads don't hit disk.
type SessionStore struct {
	path   string
	mu     sync.Mutex
	loaded bool
	cache  []StoredSession
}

// NewSessionStore creates a SessionStore that reads/writes the given file path.
func NewSessionStore(path string) *SessionStore {
	return &SessionStore{path: path}
}

// ActiveSession returns the thread ID of the active session for the given chat,
// or "" if no session is active.
func (s *SessionStore) ActiveSession(chatID int64) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()

	for _, sess := range s.cache {
		if sess.ChatID == chatID && sess.Active {
			return sess.ThreadID
		}
	}
	return ""
}

// Activate upserts a session entry and marks it as active. Any other active
// session for the same chatID is deactivated. If title is empty and the
// session already exists, the existing title is preserved.
func (s *SessionStore) Activate(chatID int64, threadID, title string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()

	now := time.Now().UTC()
	found := false
	for i := range s.cache {
		if s.cache[i].ChatID != chatID {
			continue
		}
		if s.cache[i].ThreadID == threadID {
			if title != "" {
				s.cache[i].Title = title
			}
			s.cache[i].UpdatedAt = now
			s.cache[i].Active = true
			found = true
		} else {
			s.cache[i].Active = false
		}
	}
	if !found {
		s.cache = append(s.cache, StoredSession{
			ChatID:    chatID,
			ThreadID:  threadID,
			Title:     title,
			UpdatedAt: now,
			Active:    true,
		})
		// Deactivate others for this chat (already done above for existing entries).
	}

	s.persist(s.cache)
}

// Deactivate marks the active session for the given chat as inactive.
func (s *SessionStore) Deactivate(chatID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()

	changed := false
	for i := range s.cache {
		if s.cache[i].ChatID == chatID && s.cache[i].Active {
			s.cache[i].Active = false
			changed = true
		}
	}
	if changed {
		s.persist(s.cache)
	}
}

// List returns the most recent sessions, sorted by UpdatedAt descending.
// If chatID is non-zero, only sessions belonging to that chat are returned.
func (s *SessionStore) List(chatID int64, limit int) []StoredSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()

	var sessions []StoredSession
	for _, sess := range s.cache {
		if chatID != 0 && sess.ChatID != chatID {
			continue
		}
		sessions = append(sessions, sess)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions
}

// FindByPrefix returns sessions whose ThreadID starts with the given prefix.
// If chatID is non-zero, only sessions belonging to that chat are searched.
func (s *SessionStore) FindByPrefix(chatID int64, prefix string) []StoredSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()

	var matches []StoredSession
	for _, sess := range s.cache {
		if chatID != 0 && sess.ChatID != chatID {
			continue
		}
		if strings.HasPrefix(sess.ThreadID, prefix) {
			matches = append(matches, sess)
		}
	}
	return matches
}

// ensureLoaded reads the JSON file into the in-memory cache on the first call.
// Caller must hold s.mu.
func (s *SessionStore) ensureLoaded() {
	if s.loaded {
		return
	}
	s.loaded = true
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var sessions []StoredSession
	if json.Unmarshal(data, &sessions) != nil {
		return
	}
	s.cache = sessions
}

// persist writes sessions to the JSON file atomically (write temp + rename).
// Caller must hold s.mu.
func (s *SessionStore) persist(sessions []StoredSession) {
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	tmp, err := os.CreateTemp(dir, ".sessions-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
	}
}
