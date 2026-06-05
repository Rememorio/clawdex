package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel/telegram"
	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/pairing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── updateAllowFrom tests ──

func TestUpdateAllowFrom_TelegramAddsUser(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg-bot": mustMarshal(t, onboard.TelegramChannelConfig{
				Type:      "telegram",
				AllowFrom: []int64{100},
			}),
		},
	}
	changed, err := updateAllowFrom(cfg, "tg-bot", 200, "")
	require.NoError(t, err)
	assert.True(t, changed)

	var ch onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["tg-bot"], &ch))
	assert.Equal(t, []int64{100, 200}, ch.AllowFrom)
}

func TestUpdateAllowFrom_TelegramDuplicate(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": mustMarshal(t, onboard.TelegramChannelConfig{
				Type:      "telegram",
				AllowFrom: []int64{42},
			}),
		},
	}
	changed, err := updateAllowFrom(cfg, "telegram", 42, "")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestUpdateAllowFrom_WeComAddsUser(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc-1": mustMarshal(t, onboard.WeComChannelConfig{
				Type:      "wecom",
				AllowFrom: []string{"alice"},
			}),
		},
	}
	changed, err := updateAllowFrom(cfg, "wc-1", 0, "bob")
	require.NoError(t, err)
	assert.True(t, changed)

	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc-1"], &ch))
	assert.Equal(t, []string{"alice", "bob"}, ch.AllowFrom)
}

func TestUpdateAllowFrom_WeComDuplicate(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": mustMarshal(t, onboard.WeComChannelConfig{
				Type:      "wecom",
				AllowFrom: []string{"alice"},
			}),
		},
	}
	changed, err := updateAllowFrom(cfg, "wecom", 0, "alice")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestUpdateAllowFrom_ChannelNotFound(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": mustMarshal(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	}
	changed, err := updateAllowFrom(cfg, "nonexistent", 1, "")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestUpdateAllowFrom_NilChannels(t *testing.T) {
	cfg := &onboard.FileConfig{}
	changed, err := updateAllowFrom(cfg, "any", 1, "")
	require.NoError(t, err)
	assert.False(t, changed)
	// Channels map should be initialized.
	assert.NotNil(t, cfg.Channels)
}

func TestUpdateAllowFrom_EmptyRaw(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"empty": {},
		},
	}
	changed, err := updateAllowFrom(cfg, "empty", 1, "")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestUpdateAllowFrom_InvalidJSON(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"bad": json.RawMessage(`{"type":"telegram","allow_from":"notarray"}`),
		},
	}
	_, err := updateAllowFrom(cfg, "bad", 1, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse telegram config")
}

func TestUpdateAllowFrom_UnknownChannelType(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"slack": json.RawMessage(`{"type":"slack"}`),
		},
	}
	changed, err := updateAllowFrom(cfg, "slack", 1, "")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestUpdateAllowFrom_MultiInstance(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg-1": mustMarshal(t, onboard.TelegramChannelConfig{
				Type:      "telegram",
				AllowFrom: []int64{},
			}),
			"tg-2": mustMarshal(t, onboard.TelegramChannelConfig{
				Type:      "telegram",
				AllowFrom: []int64{},
			}),
		},
	}

	// Add user 42 to tg-1 only.
	changed, err := updateAllowFrom(cfg, "tg-1", 42, "")
	require.NoError(t, err)
	assert.True(t, changed)

	// tg-2 should be untouched.
	var ch2 onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["tg-2"], &ch2))
	assert.Empty(t, ch2.AllowFrom)
}

// ── pairingListHandler tests ──

func TestPairingListHandler_Empty(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	handler := pairingListHandler(store)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/pairing/list", nil)
	handler(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var entries []pairingListEntry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
	assert.Empty(t, entries)
}

func TestPairingListHandler_WithEntries(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(123, "", "alice", "tg-bot")

	handler := pairingListHandler(store)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/pairing/list", nil)
	handler(w, r)

	assert.Equal(t, http.StatusOK, w.Code)

	var entries []pairingListEntry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, code, entries[0].Code)
	assert.Equal(t, "tg-bot", entries[0].Channel)
	assert.Equal(t, int64(123), entries[0].SenderID)
	assert.Equal(t, "alice", entries[0].Username)
}

// ── pairingApproveHandler tests ──

func TestPairingApproveHandler_MethodNotAllowed(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	handler := pairingApproveHandler(
		store, nil, nil,
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/pairing/approve?code=ABC", nil)
	handler(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestPairingApproveHandler_MissingCode(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	handler := pairingApproveHandler(
		store,
		map[string]*channelApprover{},
		nil,
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/pairing/approve", nil)
	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp approveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "missing code parameter", resp.Error)
}

func TestPairingApproveHandler_CodeNotFound(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	handler := pairingApproveHandler(
		store,
		map[string]*channelApprover{},
		nil,
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code=INVALID",
		nil,
	)
	handler(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code)
	var resp approveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "code not found or expired")
}

func TestPairingApproveHandler_TelegramApprove(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(555, "", "bob", "tg-bot2")

	var addedID int64
	approvers := map[string]*channelApprover{
		"tg-bot2": {
			addAllowedInt64: func(id int64) {
				addedID = id
			},
		},
	}

	handler := pairingApproveHandler(store, approvers, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp approveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.Equal(t, "tg-bot2", resp.Channel)
	assert.Equal(t, int64(555), resp.SenderID)
	assert.Equal(t, "bob", resp.Username)

	// Verify the approver callback was invoked.
	assert.Equal(t, int64(555), addedID)
}

func TestPairingApproveHandler_WeComApprove(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(0, "carol", "carol", "wc-1")

	var addedUser string
	approvers := map[string]*channelApprover{
		"wc-1": {
			addAllowedString: func(s string) {
				addedUser = s
			},
		},
	}

	handler := pairingApproveHandler(store, approvers, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp approveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
	assert.Equal(t, "wc-1", resp.Channel)

	// Verify the approver callback was invoked.
	assert.Equal(t, "carol", addedUser)
}

func TestPairingApproveHandler_NoApproverForChannel(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(1, "", "user", "unknown-ch")

	// No approver registered for "unknown-ch".
	handler := pairingApproveHandler(
		store,
		map[string]*channelApprover{},
		nil,
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w, r)

	// Should still succeed (approve removes the code from store).
	assert.Equal(t, http.StatusOK, w.Code)
	var resp approveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
}

func TestPairingApproveHandler_CodeConsumedOnce(t *testing.T) {
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(1, "", "user", "tg")

	handler := pairingApproveHandler(
		store,
		map[string]*channelApprover{
			"tg": {addAllowedInt64: func(int64) {}},
		},
		nil,
	)

	// First approve succeeds.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	// Second approve with same code fails.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w2, r2)
	assert.Equal(t, http.StatusNotFound, w2.Code)
}

func TestPairingApproveHandler_TgNotifierLookup(t *testing.T) {
	// Verify that non-matching channel names do not trigger
	// notification (we cannot mock SendNotification easily, but
	// we can verify no panic when tgNotifiers is nil or empty).
	store := pairing.NewStore(10 * time.Minute)
	code := store.Create(1, "", "u", "wc-1")

	handler := pairingApproveHandler(
		store,
		map[string]*channelApprover{
			"wc-1": {addAllowedString: func(string) {}},
		},
		map[string]*telegram.Driver{},
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(
		http.MethodPost,
		"/pairing/approve?code="+code,
		nil,
	)
	handler(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// ── helpers ──

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
