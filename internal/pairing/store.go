// Package pairing implements an in-memory pairing code store for
// authenticating unknown Telegram users via admin approval.
package pairing

import (
	"crypto/rand"
	"math/big"
	"sync"
	"time"
)

const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// PairingRequest represents a pending pairing request from an unknown user.
type PairingRequest struct {
	Code           string    `json:"code"`
	Channel        string    `json:"channel"`       // "telegram" or "wecom"
	SenderID       int64     `json:"sender_id"`     // Telegram user ID or WeCom hashed int64
	SenderIDStr    string    `json:"sender_id_str"` // WeCom original UserID string (empty for Telegram)
	SenderUsername string    `json:"username"`
	CreatedAt      time.Time `json:"created_at"`
}

// Store is a thread-safe in-memory store for pending pairing requests.
type Store struct {
	mu       sync.Mutex
	requests map[string]*PairingRequest // code → request
	ttl      time.Duration
}

// NewStore creates a pairing store with the given TTL for requests.
func NewStore(ttl time.Duration) *Store {
	return &Store{
		requests: make(map[string]*PairingRequest),
		ttl:      ttl,
	}
}

// Create generates a 6-char alphanumeric pairing code for the sender.
// If the sender already has a pending (non-expired) code on the same channel,
// that code is returned.
func (s *Store) Create(senderID int64, senderIDStr, senderUsername, channel string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked()

	// De-duplicate: return existing code for same sender + channel.
	for _, req := range s.requests {
		if req.SenderID == senderID && req.Channel == channel {
			return req.Code
		}
	}

	code := s.generateCode()
	s.requests[code] = &PairingRequest{
		Code:           code,
		Channel:        channel,
		SenderID:       senderID,
		SenderIDStr:    senderIDStr,
		SenderUsername: senderUsername,
		CreatedAt:      time.Now(),
	}
	return code
}

// List returns all non-expired pending pairing requests.
func (s *Store) List() []*PairingRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked()

	result := make([]*PairingRequest, 0, len(s.requests))
	for _, req := range s.requests {
		result = append(result, req)
	}
	return result
}

// Approve removes and returns the request for the given code, if it exists
// and has not expired. Returns nil, false if not found or expired.
func (s *Store) Approve(code string) (*PairingRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[code]
	if !ok {
		return nil, false
	}
	if time.Since(req.CreatedAt) > s.ttl {
		delete(s.requests, code)
		return nil, false
	}
	delete(s.requests, code)
	return req, true
}

// HasPending returns true if the given sender already has a non-expired
// pending pairing request on the specified channel.
func (s *Store) HasPending(senderID int64, channel string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, req := range s.requests {
		if req.SenderID == senderID && req.Channel == channel && time.Since(req.CreatedAt) <= s.ttl {
			return req.Code, true
		}
	}
	return "", false
}

// cleanupLocked removes expired entries. Must be called with s.mu held.
func (s *Store) cleanupLocked() {
	for code, req := range s.requests {
		if time.Since(req.CreatedAt) > s.ttl {
			delete(s.requests, code)
		}
	}
}

// generateCode produces a random 6-character alphanumeric code.
// Must be called with s.mu held (to avoid code collisions).
func (s *Store) generateCode() string {
	for {
		code := make([]byte, 6)
		for i := range code {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
			if err != nil {
				// Fallback: this should never happen with crypto/rand.
				panic("crypto/rand failed: " + err.Error())
			}
			code[i] = charset[n.Int64()]
		}
		c := string(code)
		if _, exists := s.requests[c]; !exists {
			return c
		}
	}
}
