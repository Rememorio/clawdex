package pairing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreate_GeneratesCode(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code := s.Create(123, "", "alice", "telegram")
	assert.Len(t, code, 6)
	for _, c := range code {
		assert.Contains(t, charset, string(c))
	}
}

func TestCreate_Deduplicates(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code1 := s.Create(123, "", "alice", "telegram")
	code2 := s.Create(123, "", "alice", "telegram")
	assert.Equal(t, code1, code2)
}

func TestCreate_DifferentSenders(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code1 := s.Create(123, "", "alice", "telegram")
	code2 := s.Create(456, "", "bob", "telegram")
	assert.NotEqual(t, code1, code2)
}

func TestCreate_SameSenderDifferentChannels(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code1 := s.Create(123, "", "alice", "telegram")
	code2 := s.Create(123, "alice_wecom", "alice", "wecom")
	assert.NotEqual(t, code1, code2)

	list := s.List()
	assert.Len(t, list, 2)
}

func TestList_ReturnsNonExpired(t *testing.T) {
	s := NewStore(30 * time.Minute)
	s.Create(123, "", "alice", "telegram")
	s.Create(456, "", "bob", "telegram")

	list := s.List()
	assert.Len(t, list, 2)
}

func TestList_ExcludesExpired(t *testing.T) {
	s := NewStore(1 * time.Millisecond)
	s.Create(123, "", "alice", "telegram")
	time.Sleep(5 * time.Millisecond)

	list := s.List()
	assert.Len(t, list, 0)
}

func TestApprove_Success(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code := s.Create(123, "", "alice", "telegram")

	req, ok := s.Approve(code)
	require.True(t, ok)
	assert.Equal(t, int64(123), req.SenderID)
	assert.Equal(t, "alice", req.SenderUsername)
	assert.Equal(t, "telegram", req.Channel)

	// Should be removed after approval.
	_, ok = s.Approve(code)
	assert.False(t, ok)
}

func TestApprove_Success_WeCom(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code := s.Create(789, "user123", "bob", "wecom")

	req, ok := s.Approve(code)
	require.True(t, ok)
	assert.Equal(t, int64(789), req.SenderID)
	assert.Equal(t, "user123", req.SenderIDStr)
	assert.Equal(t, "wecom", req.Channel)
}

func TestApprove_NotFound(t *testing.T) {
	s := NewStore(30 * time.Minute)
	_, ok := s.Approve("XXXXXX")
	assert.False(t, ok)
}

func TestApprove_Expired(t *testing.T) {
	s := NewStore(1 * time.Millisecond)
	code := s.Create(123, "", "alice", "telegram")
	time.Sleep(5 * time.Millisecond)

	_, ok := s.Approve(code)
	assert.False(t, ok)
}

func TestHasPending_True(t *testing.T) {
	s := NewStore(30 * time.Minute)
	code := s.Create(123, "", "alice", "telegram")

	gotCode, ok := s.HasPending(123, "telegram")
	assert.True(t, ok)
	assert.Equal(t, code, gotCode)
}

func TestHasPending_False(t *testing.T) {
	s := NewStore(30 * time.Minute)
	_, ok := s.HasPending(999, "telegram")
	assert.False(t, ok)
}

func TestHasPending_DifferentChannel(t *testing.T) {
	s := NewStore(30 * time.Minute)
	s.Create(123, "", "alice", "telegram")

	_, ok := s.HasPending(123, "wecom")
	assert.False(t, ok)
}

func TestHasPending_Expired(t *testing.T) {
	s := NewStore(1 * time.Millisecond)
	s.Create(123, "", "alice", "telegram")
	time.Sleep(5 * time.Millisecond)

	_, ok := s.HasPending(123, "telegram")
	assert.False(t, ok)
}

func TestCreate_AfterExpiry_NewCode(t *testing.T) {
	s := NewStore(1 * time.Millisecond)
	code1 := s.Create(123, "", "alice", "telegram")
	time.Sleep(5 * time.Millisecond)
	code2 := s.Create(123, "", "alice", "telegram")
	// After expiry, a new code should be generated (may or may not equal old one by chance).
	assert.Len(t, code2, 6)
	_ = code1
}
