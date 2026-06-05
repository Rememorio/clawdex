package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
)

// ── Text chunking tests ──

func TestChunkText_Short(t *testing.T) {
	chunks := chunkText("hello", 100)
	assert.Equal(t, []string{"hello"}, chunks)
}

func TestChunkText_ExactLimit(t *testing.T) {
	chunks := chunkText("abcde", 5)
	assert.Equal(t, []string{"abcde"}, chunks)
}

func TestChunkText_Split(t *testing.T) {
	chunks := chunkText("abcdefghij", 3)
	assert.Equal(t, []string{"abc", "def", "ghi", "j"}, chunks)
}

func TestChunkText_Unicode(t *testing.T) {
	chunks := chunkText("你好世界测试", 4)
	assert.Equal(t, []string{"你好世界", "测试"}, chunks)
}

func TestChunkText_Empty(t *testing.T) {
	chunks := chunkText("", 10)
	assert.Equal(t, []string{""}, chunks)
}

// ── @mention strip tests ──

func TestStripMentions_Single(t *testing.T) {
	assert.Equal(t, "hello", stripMentions("<@!bot123> hello"))
}

func TestStripMentions_Multiple(t *testing.T) {
	assert.Equal(t, "hi there", stripMentions("<@!bot1> <@user2> hi there"))
}

func TestStripMentions_NoMention(t *testing.T) {
	assert.Equal(t, "normal message", stripMentions("normal message"))
}

func TestStripMentions_OnlyMention(t *testing.T) {
	assert.Equal(t, "", stripMentions("<@!bot123>"))
}

func TestStripMentions_MentionWithoutExclaim(t *testing.T) {
	assert.Equal(t, "test", stripMentions("<@abc> test"))
}

// ── Hash function tests ──

func TestHashOpenID_Stable(t *testing.T) {
	id1 := hashOpenID("user-abc-123")
	id2 := hashOpenID("user-abc-123")
	assert.Equal(t, id1, id2)
	assert.NotZero(t, id1)
}

func TestHashOpenID_Different(t *testing.T) {
	id1 := hashOpenID("user-abc")
	id2 := hashOpenID("user-def")
	assert.NotEqual(t, id1, id2)
}

func TestHashOpenID_NonZero(t *testing.T) {
	// Even for edge-case inputs, hashOpenID should never return 0.
	id := hashOpenID("")
	assert.NotZero(t, id)
}

// ── Policy tests ──

func TestAllowDM_Open(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	assert.True(t, d.allowDM("anyone"))
}

func TestAllowDM_Allowlist_Allowed(t *testing.T) {
	d := New(Config{DMPolicy: "allowlist", AllowFrom: []string{"user-a", "user-b"}}, nil)
	assert.True(t, d.allowDM("user-a"))
	assert.True(t, d.allowDM("user-b"))
	assert.False(t, d.allowDM("user-c"))
}

func TestAllowDM_Allowlist_Empty(t *testing.T) {
	d := New(Config{DMPolicy: "allowlist", AllowFrom: []string{}}, nil)
	assert.False(t, d.allowDM("anyone"))
}

func TestAllowGroup_Open(t *testing.T) {
	d := New(Config{GroupPolicy: "open"}, nil)
	assert.True(t, d.allowGroup("any-group"))
}

func TestAllowGroup_Disabled(t *testing.T) {
	d := New(Config{GroupPolicy: "disabled"}, nil)
	assert.False(t, d.allowGroup("any-group"))
}

func TestAllowGroup_Allowlist(t *testing.T) {
	d := New(Config{GroupPolicy: "allowlist", GroupAllowFrom: []string{"group-1"}}, nil)
	assert.True(t, d.allowGroup("group-1"))
	assert.False(t, d.allowGroup("group-2"))
}

// ── Driver construction tests ──

func TestNew_Defaults(t *testing.T) {
	d := New(Config{}, nil)
	assert.Equal(t, "qqbot", d.Name())
	assert.Equal(t, "open", d.cfg.DMPolicy)
	assert.Equal(t, "allowlist", d.cfg.GroupPolicy)
	assert.Equal(t, defaultTextChunkLimit, d.cfg.TextChunkLimit)
}

func TestNew_CustomName(t *testing.T) {
	d := New(Config{Name: "mybot"}, nil)
	assert.Equal(t, "mybot", d.Name())
}

func TestNew_CustomChunkLimit(t *testing.T) {
	d := New(Config{TextChunkLimit: 1000}, nil)
	assert.Equal(t, 1000, d.cfg.TextChunkLimit)
}

// ── AddAllowedUser tests ──

func TestAddAllowedUser(t *testing.T) {
	d := New(Config{DMPolicy: "allowlist"}, nil)
	assert.False(t, d.allowDM("new-user"))

	d.AddAllowedUser("new-user")
	assert.True(t, d.allowDM("new-user"))
}

// ── MsgSeq tests ──

func TestNextMsgSeq_Increments(t *testing.T) {
	d := New(Config{}, nil)
	seq1 := d.nextMsgSeq("msg-a")
	seq2 := d.nextMsgSeq("msg-a")
	seq3 := d.nextMsgSeq("msg-b")
	assert.Equal(t, 1, seq1)
	assert.Equal(t, 2, seq2)
	assert.Equal(t, 1, seq3)
}

func TestNextMsgSeq_Concurrent(t *testing.T) {
	d := New(Config{}, nil)
	var wg sync.WaitGroup
	results := make([]int, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = d.nextMsgSeq("msg-concurrent")
		}(i)
	}
	wg.Wait()

	// All values should be unique 1..100.
	seen := make(map[int]bool)
	for _, v := range results {
		assert.False(t, seen[v], "duplicate seq: %d", v)
		seen[v] = true
	}
	assert.Len(t, seen, 100)
}

// ── Event parsing tests ──

func TestParseC2CMessageEvent(t *testing.T) {
	raw := `{
		"id": "msg-001",
		"author": {"user_openid": "user-open-123"},
		"content": "hello bot",
		"timestamp": "2026-05-26T13:00:00Z"
	}`
	var ev c2cMessageEvent
	err := json.Unmarshal([]byte(raw), &ev)
	require.NoError(t, err)
	assert.Equal(t, "msg-001", ev.ID)
	assert.Equal(t, "user-open-123", ev.Author.UserOpenID)
	assert.Equal(t, "hello bot", ev.Content)
}

func TestParseGroupMessageEvent(t *testing.T) {
	raw := `{
		"id": "msg-002",
		"author": {"member_openid": "member-456"},
		"content": "hi in group",
		"timestamp": "2026-05-26T13:00:00Z",
		"group_openid": "group-789"
	}`
	var ev groupMessageEvent
	err := json.Unmarshal([]byte(raw), &ev)
	require.NoError(t, err)
	assert.Equal(t, "msg-002", ev.ID)
	assert.Equal(t, "member-456", ev.Author.MemberOpenID)
	assert.Equal(t, "group-789", ev.GroupOpenID)
}

func TestParseC2CMessageEvent_WithAttachments(t *testing.T) {
	raw := `{
		"id": "msg-att",
		"author": {"user_openid": "user-x"},
		"content": "see image",
		"timestamp": "2026-05-26T13:00:00Z",
		"attachments": [{"content_type": "image/png", "url": "https://example.com/img.png", "filename": "img.png"}]
	}`
	var ev c2cMessageEvent
	err := json.Unmarshal([]byte(raw), &ev)
	require.NoError(t, err)
	require.Len(t, ev.Attachments, 1)
	assert.Equal(t, "image/png", ev.Attachments[0].ContentType)
	assert.Equal(t, "https://example.com/img.png", ev.Attachments[0].URL)
}

// ── Responder tests ──

func TestResponder_Reply_Chunks(t *testing.T) {
	var messages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if content, ok := body["content"].(string); ok {
			messages = append(messages, content)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0"}`)
	}))
	defer srv.Close()

	d := New(Config{TextChunkLimit: 5}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	resp := &responder{
		driver:       d,
		isGroup:      false,
		peerID:       "user-x",
		triggerMsgID: "msg-1",
	}

	err := resp.Reply(context.Background(), channel.Message{}, "abcdefghij")
	require.NoError(t, err)
	assert.Equal(t, []string{"abcde", "fghij"}, messages)
}

func TestResponder_Reply_GroupPath(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0"}`)
	}))
	defer srv.Close()

	d := New(Config{}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	resp := &responder{
		driver:       d,
		isGroup:      true,
		peerID:       "group-abc",
		triggerMsgID: "msg-1",
	}

	err := resp.Reply(context.Background(), channel.Message{}, "hi")
	require.NoError(t, err)
	assert.Equal(t, "/v2/groups/group-abc/messages", receivedPath)
}

// ── Handler dispatch tests ──

func TestHandleC2CMessage_DispatchesToHandler(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{DMPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(c2cMessageEvent{
		ID:      "msg-100",
		Author:  eventAuthor{UserOpenID: "sender-abc"},
		Content: "  test dm  ",
	})

	d.handleC2CMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "test dm", received.Text) // trimmed
	assert.Equal(t, d.name, received.Channel)
	assert.Equal(t, "", received.ChatType)
}

func TestHandleGroupMessage_DispatchesToHandler(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-200",
		Author:      eventAuthor{MemberOpenID: "member-xyz"},
		Content:     "group hello",
		GroupOpenID: "group-001",
	})

	d.handleGroupMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "group hello", received.Text)
	assert.Equal(t, "group", received.ChatType)
	assert.Equal(t, hashOpenID("group-001"), received.ChatID)
}

func TestHandleC2CMessage_RejectedByPolicy(t *testing.T) {
	called := false
	d := New(Config{DMPolicy: "allowlist", AllowFrom: []string{"allowed-user"}}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		called = true
	}}

	raw := mustMarshal(c2cMessageEvent{
		ID:      "msg-300",
		Author:  eventAuthor{UserOpenID: "blocked-user"},
		Content: "should be blocked",
	})

	d.handleC2CMessage(context.Background(), raw)
	assert.False(t, called)
}

func TestHandleGroupMessage_RejectedByPolicy(t *testing.T) {
	called := false
	d := New(Config{GroupPolicy: "allowlist", GroupAllowFrom: []string{"allowed-group"}}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		called = true
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-400",
		Author:      eventAuthor{MemberOpenID: "member-x"},
		Content:     "rejected",
		GroupOpenID: "other-group",
	})

	d.handleGroupMessage(context.Background(), raw)
	assert.False(t, called)
}

// ── Helpers ──

type mockHandler struct {
	handleFn func(ctx context.Context, msg channel.Message, resp channel.Responder)
}

func (h *mockHandler) Handle(ctx context.Context, msg channel.Message, resp channel.Responder) {
	if h.handleFn != nil {
		h.handleFn(ctx, msg, resp)
	}
}

// ── Additional coverage tests (appended) ──

func TestHandleC2CMessage_InvalidJSON(t *testing.T) {
	called := false
	d := New(Config{DMPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		called = true
	}}

	// Invalid JSON should not dispatch to handler.
	d.handleC2CMessage(context.Background(), json.RawMessage(`{invalid`))
	assert.False(t, called)
}

func TestHandleGroupMessage_InvalidJSON(t *testing.T) {
	called := false
	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		called = true
	}}

	d.handleGroupMessage(context.Background(), json.RawMessage(`{invalid`))
	assert.False(t, called)
}

func TestHandleC2CMessage_FallbackToAuthorID(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{DMPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(c2cMessageEvent{
		ID:      "msg-fallback",
		Author:  eventAuthor{ID: "fallback-id", UserOpenID: ""},
		Content: "test",
	})

	d.handleC2CMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "test", received.Text)
	assert.Equal(t, hashOpenID("fallback-id"), received.ChatID)
}

func TestHandleGroupMessage_FallbackToUserOpenID(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-fallback2",
		Author:      eventAuthor{MemberOpenID: "", UserOpenID: "user-open-fallback", ID: "last-resort"},
		Content:     "group test",
		GroupOpenID: "group-abc",
	})

	d.handleGroupMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "group test", received.Text)
	assert.Equal(t, hashOpenID("user-open-fallback"), received.SenderID)
}

func TestHandleGroupMessage_FallbackToAuthorID(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-fallback3",
		Author:      eventAuthor{MemberOpenID: "", UserOpenID: "", ID: "last-resort-id"},
		Content:     "test",
		GroupOpenID: "group-xyz",
	})

	d.handleGroupMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, hashOpenID("last-resort-id"), received.SenderID)
}

func TestHandleGroupMessage_StripsMentionTags(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-mention",
		Author:      eventAuthor{MemberOpenID: "sender"},
		Content:     "<@!bot123> <@user456> actual question here",
		GroupOpenID: "group-1",
	})

	d.handleGroupMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "actual question here", received.Text)
}

func TestAllowDM_Pairing_AllowedUser(t *testing.T) {
	d := New(Config{DMPolicy: "pairing", AllowFrom: []string{"known-user"}}, nil)
	assert.True(t, d.allowDM("known-user"))
}

func TestAllowDM_Pairing_UnknownUser(t *testing.T) {
	d := New(Config{DMPolicy: "pairing", AllowFrom: []string{"known-user"}}, nil)
	assert.False(t, d.allowDM("unknown-user"))
}

func TestAllowDM_UnknownPolicy(t *testing.T) {
	d := New(Config{}, nil)
	d.cfg.DMPolicy = "unknown"
	assert.True(t, d.allowDM("anyone"))
}

func TestAllowGroup_DefaultPolicy(t *testing.T) {
	d := New(Config{}, nil)
	d.cfg.GroupPolicy = "something-else"
	// Default case falls through to allowlist check
	assert.False(t, d.allowGroup("any-group"))
}

func TestResponder_Typing_Group(t *testing.T) {
	d := New(Config{}, nil)
	resp := &responder{driver: d, isGroup: true, peerID: "group-1"}
	err := resp.Typing(context.Background(), channel.Message{})
	assert.NoError(t, err) // groups don't support typing
}

func TestResponder_Reply_ProactiveModeAfterLimit(t *testing.T) {
	var receivedBodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		receivedBodies = append(receivedBodies, body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0"}`)
	}))
	defer srv.Close()

	d := New(Config{TextChunkLimit: 1000}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	resp := &responder{
		driver:       d,
		isGroup:      false,
		peerID:       "user-x",
		triggerMsgID: "trigger-1",
	}

	// Send 5 messages to exceed passiveReplyLimit (4)
	for i := 0; i < 5; i++ {
		err := resp.Reply(context.Background(), channel.Message{}, "msg")
		require.NoError(t, err)
	}

	// First 4 should have msg_id set, 5th should have it omitted
	require.Len(t, receivedBodies, 5)
	for i := 0; i < 4; i++ {
		assert.Equal(t, "trigger-1", receivedBodies[i]["msg_id"])
	}
	// The 5th message: msg_id should be empty string or nil (omitted from JSON)
	msgID5, _ := receivedBodies[4]["msg_id"].(string)
	assert.Equal(t, "", msgID5)
}

func TestHandleDispatch_Ready(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	state := &wsState{}

	readyPayload := mustMarshal(readyData{
		SessionID: "sess-test-123",
		User: struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Bot      bool   `json:"bot"`
		}{ID: "bot-1", Username: "TestBot", Bot: true},
	})

	payload := wsPayload{
		Op: opDispatch,
		T:  "READY",
		D:  readyPayload,
	}

	d.handleDispatch(context.Background(), payload, state)

	sessID, _ := state.get()
	assert.Equal(t, "sess-test-123", sessID)
}

func TestHandleDispatch_Resumed(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	state := &wsState{}
	state.setSession("sess-existing")

	payload := wsPayload{
		Op: opDispatch,
		T:  "RESUMED",
		D:  json.RawMessage(`{}`),
	}

	// Should not panic or change state
	d.handleDispatch(context.Background(), payload, state)

	sessID, _ := state.get()
	assert.Equal(t, "sess-existing", sessID)
}

func TestHandleDispatch_UnknownEvent(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	state := &wsState{}

	payload := wsPayload{
		Op: opDispatch,
		T:  "UNKNOWN_EVENT_TYPE",
		D:  json.RawMessage(`{"key":"value"}`),
	}

	// Should not panic
	d.handleDispatch(context.Background(), payload, state)
}

func TestHandleDispatch_ReadyInvalidJSON(t *testing.T) {
	d := New(Config{DMPolicy: "open"}, nil)
	state := &wsState{}

	payload := wsPayload{
		Op: opDispatch,
		T:  "READY",
		D:  json.RawMessage(`{invalid`),
	}

	// Should not panic; session should remain empty
	d.handleDispatch(context.Background(), payload, state)
	sessID, _ := state.get()
	assert.Equal(t, "", sessID)
}

func TestContains(t *testing.T) {
	assert.True(t, contains([]string{"a", "b", "c"}, "b"))
	assert.False(t, contains([]string{"a", "b", "c"}, "d"))
	assert.False(t, contains(nil, "a"))
	assert.False(t, contains([]string{}, "a"))
}

// ── Additional coverage: ReplyWithMedia, handleDispatch full, attachments ──

func TestResponder_ReplyWithMedia_C2C(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0","file_info":"fi"}`)
	}))
	defer srv.Close()

	d := New(Config{TextChunkLimit: 1000}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	dir := t.TempDir()
	imgPath := dir + "/test.png"
	require.NoError(t, os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4E, 0x47}, 0o644))

	resp := &responder{
		driver:       d,
		isGroup:      false,
		peerID:       "user-x",
		triggerMsgID: "msg-t",
	}

	err := resp.ReplyWithMedia(context.Background(), channel.Message{}, "caption", []string{imgPath})
	require.NoError(t, err)
}

func TestResponder_ReplyWithMedia_Group(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0","file_info":"fi"}`)
	}))
	defer srv.Close()

	d := New(Config{TextChunkLimit: 1000}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	dir := t.TempDir()
	imgPath := dir + "/test.jpg"
	require.NoError(t, os.WriteFile(imgPath, []byte{0xFF, 0xD8, 0xFF}, 0o644))

	resp := &responder{
		driver:       d,
		isGroup:      true,
		peerID:       "grp-abc",
		triggerMsgID: "msg-g",
	}

	err := resp.ReplyWithMedia(context.Background(), channel.Message{}, "", []string{imgPath})
	require.NoError(t, err)
}

func TestHandleC2CMessage_WithAttachments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4E, 0x47})
	}))
	defer srv.Close()

	var received channel.Message
	var mu sync.Mutex

	d := New(Config{DMPolicy: "open"}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(c2cMessageEvent{
		ID:      "msg-att",
		Author:  eventAuthor{UserOpenID: "sender-att"},
		Content: "with image",
		Attachments: []attachment{
			{ContentType: "image/png", URL: srv.URL + "/img.png"},
		},
	})

	d.handleC2CMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "with image", received.Text)
	assert.NotEmpty(t, received.MediaPaths)
}

func TestHandleGroupMessage_WithAttachments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF})
	}))
	defer srv.Close()

	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	raw := mustMarshal(groupMessageEvent{
		ID:          "msg-grp-att",
		Author:      eventAuthor{MemberOpenID: "member-att"},
		Content:     "<@!bot> with attachment",
		GroupOpenID: "grp-att",
		Attachments: []attachment{
			{ContentType: "image/jpeg", URL: srv.URL + "/img.jpg"},
		},
	})

	d.handleGroupMessage(context.Background(), raw)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "with attachment", received.Text)
	assert.NotEmpty(t, received.MediaPaths)
}

func TestHandleDispatch_C2CMessage(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{DMPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	state := &wsState{}
	seq := 10
	payload := wsPayload{
		Op: opDispatch,
		T:  "C2C_MESSAGE_CREATE",
		S:  &seq,
		D:  mustMarshal(c2cMessageEvent{ID: "d-msg", Author: eventAuthor{UserOpenID: "d-user"}, Content: "dispatch test"}),
	}

	d.handleDispatch(context.Background(), payload, state)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "dispatch test", received.Text)
}

func TestHandleDispatch_GroupAtMessage(t *testing.T) {
	var received channel.Message
	var mu sync.Mutex

	d := New(Config{GroupPolicy: "open"}, nil)
	d.handler = &mockHandler{handleFn: func(ctx context.Context, msg channel.Message, resp channel.Responder) {
		mu.Lock()
		received = msg
		mu.Unlock()
	}}

	state := &wsState{}
	seq := 11
	payload := wsPayload{
		Op: opDispatch,
		T:  "GROUP_AT_MESSAGE_CREATE",
		S:  &seq,
		D:  mustMarshal(groupMessageEvent{ID: "g-msg", Author: eventAuthor{MemberOpenID: "g-member"}, Content: "group dispatch", GroupOpenID: "grp-d"}),
	}

	d.handleDispatch(context.Background(), payload, state)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "group dispatch", received.Text)
	assert.Equal(t, "group", received.ChatType)
}

func TestResponder_Reply_EmptyMsgID(t *testing.T) {
	var receivedBodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		receivedBodies = append(receivedBodies, body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"r","timestamp":"0"}`)
	}))
	defer srv.Close()

	d := New(Config{TextChunkLimit: 1000}, nil)
	d.api.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	d.api.token = "tok"
	d.api.expiresAt = time.Now().Add(1 * time.Hour)

	resp := &responder{
		driver:       d,
		isGroup:      false,
		peerID:       "user-x",
		triggerMsgID: "",
	}

	err := resp.Reply(context.Background(), channel.Message{}, "hello")
	require.NoError(t, err)
	require.Len(t, receivedBodies, 1)
	msgID, _ := receivedBodies[0]["msg_id"].(string)
	assert.Equal(t, "", msgID)
}
