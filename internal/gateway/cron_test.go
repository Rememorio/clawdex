package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
)

type cronToolHTTPResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func newCronTestService(t *testing.T) *Service {
	t.Helper()
	svc := New(&codex.Client{Sandbox: "workspace-write"}, 1, "off")
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc.SetCron(cronjob.New(cronjob.Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			return nil
		},
	}))
	return svc
}

func callCronTool(t *testing.T, svc *Service, body map[string]any) cronToolHTTPResponse {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/cron/tool", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	svc.handleCronTool(rec, req)

	var out cronToolHTTPResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestCronToolAddAndListAreScopedToCurrentChat(t *testing.T) {
	svc := newCronTestService(t)
	token := svc.newCronContext(channel.Message{
		Channel: "telegram",
		ChatID:  42,
		Target:  "42",
		Text:    "remind me every minute",
	})

	add := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "add",
		"job": map[string]any{
			"name":     "water",
			"schedule": map[string]any{"kind": "every", "every_seconds": 60},
			"payload":  map[string]any{"kind": "message", "text": "drink water"},
		},
	})
	require.True(t, add.OK, add.Error)
	var addResult struct {
		Job cronjob.Job `json:"job"`
	}
	require.NoError(t, json.Unmarshal(add.Result, &addResult))
	assert.Equal(t, "telegram", addResult.Job.Delivery.Channel)
	assert.Equal(t, int64(42), addResult.Job.Delivery.ChatID)
	assert.Equal(t, "42", addResult.Job.Delivery.Target)
	assert.Equal(t, int64(42), addResult.Job.ScopeID)

	list := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "list",
	})
	require.True(t, list.OK, list.Error)
	var listResult struct {
		Jobs []cronjob.Job `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(list.Result, &listResult))
	require.Len(t, listResult.Jobs, 1)
	assert.Equal(t, addResult.Job.ID, listResult.Jobs[0].ID)

	otherToken := svc.newCronContext(channel.Message{
		Channel: "telegram",
		ChatID:  99,
		Target:  "99",
	})
	otherList := callCronTool(t, svc, map[string]any{
		"token":  otherToken,
		"action": "list",
	})
	require.True(t, otherList.OK, otherList.Error)
	var otherResult struct {
		Jobs []cronjob.Job `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(otherList.Result, &otherResult))
	assert.Empty(t, otherResult.Jobs)
}

func TestCronToolRejectsInvalidToken(t *testing.T) {
	svc := newCronTestService(t)
	out := callCronTool(t, svc, map[string]any{
		"token":  "missing",
		"action": "list",
	})
	assert.False(t, out.OK)
	assert.Contains(t, out.Error, "invalid or expired")
}

type testProactiveSender struct {
	name   string
	target channel.DeliveryTarget
	text   string
}

func (s *testProactiveSender) Name() string { return s.name }

func (s *testProactiveSender) SendText(ctx context.Context, target channel.DeliveryTarget, text string) error {
	s.target = target
	s.text = text
	return nil
}

func TestDeliverCronUsesRegisteredSender(t *testing.T) {
	svc := New(&codex.Client{}, 1, "off")
	sender := &testProactiveSender{name: "telegram"}
	svc.RegisterSender(sender)

	target := channel.DeliveryTarget{Channel: "telegram", ChatID: 42, Target: "42"}
	require.NoError(t, svc.DeliverCron(context.Background(), target, "hello"))
	assert.Equal(t, target, sender.target)
	assert.Equal(t, "hello", sender.text)
}

func TestCronToolNotifyDeliversToContextTarget(t *testing.T) {
	svc := newCronTestService(t)
	sender := &testProactiveSender{name: "telegram"}
	svc.RegisterSender(sender)
	token := svc.newCronContext(channel.Message{
		Channel: "telegram",
		ChatID:  42,
		Target:  "42",
	})

	out := callCronTool(t, svc, map[string]any{
		"token":   token,
		"action":  "notify",
		"title":   "Report",
		"content": "hello",
	})

	require.True(t, out.OK, out.Error)
	assert.Equal(t, channel.DeliveryTarget{Channel: "telegram", ChatID: 42, Target: "42"}, sender.target)
	assert.Equal(t, "Report\n\nhello", sender.text)
	assert.True(t, svc.consumeCronNotified(token))
	assert.False(t, svc.consumeCronNotified(token))
}

func TestCronAgentScopeIDIsStableAndIsolated(t *testing.T) {
	job := cronjob.Job{
		ID:       "cron_1111111111111111",
		ScopeID:  42,
		Delivery: channel.DeliveryTarget{ChatID: 42},
	}

	first := cronAgentScopeID(job)
	second := cronAgentScopeID(job)

	assert.Equal(t, first, second)
	assert.NotZero(t, first)
	assert.NotEqual(t, job.ScopeID, first)
	assert.NotEqual(t, job.Delivery.ChatID, first)
}

func TestCronAgentScopeIDFallsBackForUnsavedJob(t *testing.T) {
	job := cronjob.Job{
		ScopeID:  42,
		Delivery: channel.DeliveryTarget{ChatID: 99},
	}

	assert.Equal(t, int64(42), cronAgentScopeID(job))
}

func TestCronAgentOutputError(t *testing.T) {
	assert.NoError(t, cronAgentOutputError("normal report"))
	assert.ErrorContains(t, cronAgentOutputError("codex failed: exit status 1"), "exit status 1")
	assert.ErrorContains(t, cronAgentOutputError("codex command timeout"), "timeout")
	assert.ErrorContains(t, cronAgentOutputError("failed to create temporary directory: denied"), "temporary directory")
}
