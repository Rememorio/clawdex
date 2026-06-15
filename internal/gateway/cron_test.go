package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestCronToolRejectsControlOnlyCreatePayload(t *testing.T) {
	svc := newCronTestService(t)
	token := svc.newCronContext(channel.Message{
		Channel: "wecom",
		ChatID:  42,
		Target:  "single:T64560027A",
		Text:    "create and run now",
	})

	add := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "add",
		"job": map[string]any{
			"name":     "bad",
			"schedule": map[string]any{"kind": "cron", "expr": "0 10 * * *"},
			"payload":  map[string]any{"kind": "agent", "text": "吧，然后现在就触发一次"},
		},
	})

	assert.False(t, add.OK)
	assert.Contains(t, add.Error, "payload text")

	list := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "list",
	})
	require.True(t, list.OK, list.Error)
	var listResult struct {
		Jobs []cronjob.Job `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(list.Result, &listResult))
	assert.Empty(t, listResult.Jobs)
}

func TestCronToolRejectsControlOnlyPatchPayload(t *testing.T) {
	svc := newCronTestService(t)
	msg := channel.Message{
		Channel: "wecom",
		ChatID:  42,
		Target:  "single:T64560027A",
	}
	token := svc.newCronContext(msg)
	job, err := svc.cron.Add(context.Background(), cronjob.CreateJob{
		Name:     "ok",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleEvery, EverySeconds: 60},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: "drink water"},
		Delivery: deliveryTargetFromMessage(msg),
		ScopeID:  sessionScopeID(msg),
	})
	require.NoError(t, err)

	update := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "update",
		"id":     job.ID,
		"patch": map[string]any{
			"payload": map[string]any{"kind": "message", "text": "的定时任务，然后现在触发一次"},
		},
	})

	assert.False(t, update.OK)
	assert.Contains(t, update.Error, "payload text")

	got, ok, err := svc.cron.Get(context.Background(), job.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "drink water", got.Payload.Text)
}

func TestCronToolDeliveryMatchingRequiresCanonicalTarget(t *testing.T) {
	svc := newCronTestService(t)
	canonicalToken := svc.newCronContext(channel.Message{
		Channel:  "Ardelia",
		ChatID:   -1,
		ChatType: "group",
		Target:   "group:wrk-chat",
		Text:     "remind me every minute",
	})

	add := callCronTool(t, svc, map[string]any{
		"token":  canonicalToken,
		"action": "add",
		"job": map[string]any{
			"name":     "water",
			"schedule": map[string]any{"kind": "every", "every_seconds": 60},
			"payload":  map[string]any{"kind": "message", "text": "drink water"},
		},
	})
	require.True(t, add.OK, add.Error)

	list := callCronTool(t, svc, map[string]any{
		"token":  canonicalToken,
		"action": "list",
	})
	require.True(t, list.OK, list.Error)
	var listResult struct {
		Jobs []cronjob.Job `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(list.Result, &listResult))
	require.Len(t, listResult.Jobs, 1)
	assert.Equal(t, "water", listResult.Jobs[0].Name)

	legacyToken := svc.newCronContext(channel.Message{
		Channel:  "Ardelia",
		ChatID:   -2,
		ChatType: "group",
		Target:   "wrk-chat",
	})
	legacyList := callCronTool(t, svc, map[string]any{
		"token":  legacyToken,
		"action": "list",
	})
	require.True(t, legacyList.OK, legacyList.Error)
	var legacyListResult struct {
		Jobs []cronjob.Job `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(legacyList.Result, &legacyListResult))
	assert.Empty(t, legacyListResult.Jobs)

	singleToken := svc.newCronContext(channel.Message{
		Channel:  "Ardelia",
		ChatID:   -3,
		ChatType: "single",
		Target:   "single:T64560027A",
	})
	singleJob, err := svc.cron.Add(context.Background(), cronjob.CreateJob{
		Name:     "dm",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleEvery, EverySeconds: 60},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: "hello"},
		Delivery: channel.DeliveryTarget{
			Channel:  "Ardelia",
			ChatID:   -4,
			ChatType: "single",
			Target:   "single:T64560027A",
		},
	})
	require.NoError(t, err)

	get := callCronTool(t, svc, map[string]any{
		"token":  singleToken,
		"action": "get",
		"job_id": singleJob.ID,
	})
	assert.True(t, get.OK, get.Error)
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

func TestCronToolRunStartsJobAsynchronously(t *testing.T) {
	svc := New(&codex.Client{}, 1, "off")
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	delivered := make(chan string, 1)
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()
	svc.SetCron(cronjob.New(cronjob.Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		RunAgent: func(ctx context.Context, job cronjob.Job) (string, error) {
			started <- struct{}{}
			<-release
			return "finished", ctx.Err()
		},
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			delivered <- text
			return nil
		},
	}))

	msg := channel.Message{
		Channel:  "Ardelia",
		ChatID:   -1,
		ChatType: "single",
		Target:   "single:T64560027A",
	}
	token := svc.newCronContext(msg)
	job, err := svc.cron.Add(context.Background(), cronjob.CreateJob{
		Name:     "agent",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleEvery, EverySeconds: 60},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadAgent, Text: "build a report"},
		Delivery: deliveryTargetFromMessage(msg),
		ScopeID:  sessionScopeID(msg),
	})
	require.NoError(t, err)

	start := time.Now()
	run := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "run",
		"job_id": job.ID,
	})
	require.True(t, run.OK, run.Error)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	var result cronjob.RunResult
	require.NoError(t, json.Unmarshal(run.Result, &result))
	assert.Equal(t, job.ID, result.JobID)
	assert.Equal(t, cronjob.StatusRunning, result.Status)

	again := callCronTool(t, svc, map[string]any{
		"token":  token,
		"action": "run",
		"job_id": job.ID,
	})
	require.True(t, again.OK, again.Error)
	require.NoError(t, json.Unmarshal(again.Result, &result))
	assert.Equal(t, cronjob.StatusRunning, result.Status)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start")
	}
	release <- struct{}{}

	select {
	case text := <-delivered:
		assert.Equal(t, "finished", text)
	case <-time.After(time.Second):
		t.Fatal("agent result was not delivered")
	}
	require.Eventually(t, func() bool {
		got, ok, err := svc.cron.Get(context.Background(), job.ID)
		return err == nil && ok && got.State.LastStatus == cronjob.StatusOK && got.State.RunningAt == nil
	}, time.Second, 10*time.Millisecond)
}

type testProactiveSender struct {
	name   string
	target channel.DeliveryTarget
	text   string
	texts  []string
}

func (s *testProactiveSender) Name() string { return s.name }

func (s *testProactiveSender) SendText(ctx context.Context, target channel.DeliveryTarget, text string) error {
	s.target = target
	s.text = text
	s.texts = append(s.texts, text)
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

func TestParseCronAgentJSONMessages(t *testing.T) {
	out := "```json\n" + `{"messages":[{"title":"Batch 1","text":"hello"},{"title":"Batch 2","content":"world"}]}` + "\n```"

	messages := parseCronAgentMessages(out)

	require.Equal(t, []string{
		"Batch 1\n\nhello",
		"Batch 2\n\nworld",
	}, messages)
}

func TestParseCronAgentDelimitedMessages(t *testing.T) {
	out := cronAgentMessageDelimiter + "\nfirst\n" + cronAgentMessageDelimiter + "\nsecond\n"

	messages := parseCronAgentMessages(out)

	require.Equal(t, []string{"first", "second"}, messages)
}

func TestParseCronAgentMessageAliases(t *testing.T) {
	out := `{"parts":[{"title":"Part","text":"body"}]}`

	messages := parseCronAgentMessages(out)

	require.Equal(t, []string{"Part\n\nbody"}, messages)
}

func TestRunCronAgentDeliversEnvelopeMessages(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	scriptContent := `#!/bin/sh
echo "$@" > ` + argsFile + `
echo '{"type":"thread.started","thread_id":"cron-agent"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"{\"messages\":[{\"title\":\"One\",\"text\":\"first\"},{\"title\":\"Two\",\"text\":\"second\"}]}"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	svc := New(&codex.Client{
		WorkDir:        t.TempDir(),
		Timeout:        10 * time.Second,
		CronMCPEnabled: true,
		Store:          codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "off")
	sender := &testProactiveSender{name: "telegram"}
	svc.RegisterSender(sender)
	job := cronjob.Job{
		ID:      "cron_agent",
		Payload: cronjob.Payload{Kind: cronjob.PayloadAgent, Text: "send two messages"},
		Delivery: channel.DeliveryTarget{
			Channel: "telegram",
			ChatID:  42,
			Target:  "42",
		},
	}

	text, err := svc.RunCronAgent(context.Background(), job)

	assert.ErrorIs(t, err, cronjob.ErrAlreadyDelivered)
	assert.Empty(t, text)
	assert.Equal(t, []string{"One\n\nfirst", "Two\n\nsecond"}, sender.texts)
	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.NotContains(t, string(argsData), "mcp_servers.clawdex_cron")
}
