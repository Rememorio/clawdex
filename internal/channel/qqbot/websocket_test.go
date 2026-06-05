package qqbot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── WebSocket payload parsing tests ──

func TestWSPayload_ParseHello(t *testing.T) {
	raw := `{"op":10,"d":{"heartbeat_interval":41250}}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, opHello, p.Op)

	var hello helloData
	err = json.Unmarshal(p.D, &hello)
	require.NoError(t, err)
	assert.Equal(t, 41250, hello.HeartbeatInterval)
}

func TestWSPayload_ParseReady(t *testing.T) {
	raw := `{"op":0,"s":1,"t":"READY","d":{"version":1,"session_id":"sess-abc","user":{"id":"bot-id","username":"TestBot","bot":true},"shard":[0,1]}}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, opDispatch, p.Op)
	assert.Equal(t, "READY", p.T)
	require.NotNil(t, p.S)
	assert.Equal(t, 1, *p.S)

	var ready readyData
	err = json.Unmarshal(p.D, &ready)
	require.NoError(t, err)
	assert.Equal(t, "sess-abc", ready.SessionID)
	assert.Equal(t, "TestBot", ready.User.Username)
	assert.True(t, ready.User.Bot)
}

func TestWSPayload_ParseDispatch_C2CMessage(t *testing.T) {
	raw := `{"op":0,"s":5,"t":"C2C_MESSAGE_CREATE","d":{"id":"msg-123","author":{"user_openid":"sender-xyz"},"content":"hello","timestamp":"2026-05-26T10:00:00Z"}}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, "C2C_MESSAGE_CREATE", p.T)

	var ev c2cMessageEvent
	err = json.Unmarshal(p.D, &ev)
	require.NoError(t, err)
	assert.Equal(t, "msg-123", ev.ID)
	assert.Equal(t, "sender-xyz", ev.Author.UserOpenID)
	assert.Equal(t, "hello", ev.Content)
}

func TestWSPayload_ParseDispatch_GroupMessage(t *testing.T) {
	raw := `{"op":0,"s":8,"t":"GROUP_AT_MESSAGE_CREATE","d":{"id":"msg-456","author":{"member_openid":"mem-abc"},"content":"@bot hi","timestamp":"2026-05-26T10:00:00Z","group_openid":"grp-xyz"}}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, "GROUP_AT_MESSAGE_CREATE", p.T)

	var ev groupMessageEvent
	err = json.Unmarshal(p.D, &ev)
	require.NoError(t, err)
	assert.Equal(t, "msg-456", ev.ID)
	assert.Equal(t, "mem-abc", ev.Author.MemberOpenID)
	assert.Equal(t, "grp-xyz", ev.GroupOpenID)
}

func TestWSPayload_HeartbeatAck(t *testing.T) {
	raw := `{"op":11}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, opHeartbeatAck, p.Op)
}

func TestWSPayload_Reconnect(t *testing.T) {
	raw := `{"op":7}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, opReconnect, p.Op)
}

func TestWSPayload_InvalidSession(t *testing.T) {
	raw := `{"op":9,"d":false}`
	var p wsPayload
	err := json.Unmarshal([]byte(raw), &p)
	require.NoError(t, err)
	assert.Equal(t, opInvalidSession, p.Op)
}

// ── Identify/Resume payload marshalling tests ──

func TestIdentifyPayload_Marshal(t *testing.T) {
	p := identifyPayload{
		Token:   "QQBot test-token",
		Intents: defaultIntents,
		Shard:   []int{0, 1},
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)

	var decoded map[string]any
	json.Unmarshal(data, &decoded)
	assert.Equal(t, "QQBot test-token", decoded["token"])
	assert.Equal(t, []any{float64(0), float64(1)}, decoded["shard"])
}

func TestResumePayload_Marshal(t *testing.T) {
	p := resumePayload{
		Token:     "QQBot test-token",
		SessionID: "sess-xyz",
		Seq:       42,
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)

	var decoded map[string]any
	json.Unmarshal(data, &decoded)
	assert.Equal(t, "QQBot test-token", decoded["token"])
	assert.Equal(t, "sess-xyz", decoded["session_id"])
	assert.Equal(t, float64(42), decoded["seq"])
}

// ── wsState tests ──

func TestWSState_UpdateAndGet(t *testing.T) {
	s := &wsState{}

	sessID, seq := s.get()
	assert.Equal(t, "", sessID)
	assert.Equal(t, 0, seq)

	s.setSession("sess-1")
	s.update(5)

	sessID, seq = s.get()
	assert.Equal(t, "sess-1", sessID)
	assert.Equal(t, 5, seq)
}

func TestWSState_Reset(t *testing.T) {
	s := &wsState{}
	s.setSession("sess-x")
	s.update(10)

	s.reset()

	sessID, seq := s.get()
	assert.Equal(t, "", sessID)
	assert.Equal(t, 0, seq)
}

// ── MustMarshal tests ──

func TestMustMarshal(t *testing.T) {
	data := mustMarshal(map[string]int{"a": 1})
	assert.Equal(t, `{"a":1}`, string(data))
}
