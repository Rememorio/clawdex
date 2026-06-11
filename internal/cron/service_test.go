package cron

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
)

func TestServiceAddRunNowAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	var delivered []string

	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			delivered = append(delivered, target.Channel+":"+text)
			return nil
		},
	})

	job, err := svc.Add(ctx, CreateJob{
		Name:     "ping",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "hello"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		ScopeID:  42,
	})
	require.NoError(t, err)
	assert.Equal(t, "ping", job.Name)
	require.NotNil(t, job.State.NextRunAt)
	assert.Equal(t, now.Add(time.Minute), *job.State.NextRunAt)

	result, err := svc.RunNow(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOK, result.Status)
	assert.True(t, result.Delivered)
	assert.Equal(t, []string{"test:hello"}, delivered)

	got, ok, err := svc.Get(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, got.State.RunCount)
	assert.Equal(t, StatusOK, got.State.LastStatus)
	assert.Nil(t, got.State.RunningAt)

	renamed := "renamed"
	enabled := false
	updated, err := svc.Update(ctx, job.ID, PatchJob{Name: &renamed, Enabled: &enabled})
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Name)
	assert.False(t, updated.Enabled)
	assert.Nil(t, updated.State.NextRunAt)

	removed, err := svc.Remove(ctx, job.ID)
	require.NoError(t, err)
	assert.True(t, removed)

	jobs, err := svc.List(ctx, true)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestServiceRunNowAgentPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	var delivered []string
	var seen Job

	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		RunAgent: func(ctx context.Context, job Job) (string, error) {
			seen = job
			return "agent result: " + job.Payload.Text, nil
		},
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			delivered = append(delivered, text)
			return nil
		},
	})

	job, err := svc.Add(ctx, CreateJob{
		Name:     "agent",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadAgent, Text: "check the build"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		ScopeID:  99,
	})
	require.NoError(t, err)

	result, err := svc.RunNow(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOK, result.Status)
	assert.Equal(t, int64(99), seen.ScopeID)
	assert.Equal(t, []string{"agent result: check the build"}, delivered)
}

func TestServiceRunNowAgentErrorDeliversFailureNotice(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	var delivered []string

	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		RunAgent: func(ctx context.Context, job Job) (string, error) {
			return "", errors.New("codex failed: exit status 1")
		},
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			delivered = append(delivered, text)
			return nil
		},
	})

	job, err := svc.Add(ctx, CreateJob{
		Name:     "agent",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadAgent, Text: "check the build"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		ScopeID:  99,
	})
	require.NoError(t, err)

	result, err := svc.RunNow(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusError, result.Status)
	assert.True(t, result.Delivered)
	assert.Contains(t, result.Error, "exit status 1")
	require.Len(t, delivered, 1)
	assert.Contains(t, delivered[0], "Scheduled job failed: agent")
	assert.Contains(t, delivered[0], "codex failed: exit status 1")

	got, ok, err := svc.Get(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StatusError, got.State.LastStatus)
	assert.True(t, got.State.Delivered)
}

func TestServiceOneShotDisablesAfterRun(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			return nil
		},
	})

	job, err := svc.Add(ctx, CreateJob{
		Name:     "once",
		Schedule: Schedule{Kind: ScheduleAt, At: now.Add(time.Hour).Format(time.RFC3339)},
		Payload:  Payload{Kind: PayloadMessage, Text: "one shot"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	})
	require.NoError(t, err)

	result, err := svc.RunNow(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOK, result.Status)

	got, ok, err := svc.Get(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, got.Enabled)
	assert.Nil(t, got.State.NextRunAt)
	assert.NotNil(t, got.State.CompletedAt)
}

func TestServiceValidatesRequiredFields(t *testing.T) {
	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) },
	})

	_, err := svc.Add(context.Background(), CreateJob{
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "hello"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delivery.channel is required")
}

func TestServiceStatusListAndLookup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
	})

	slow, err := svc.Add(ctx, CreateJob{
		Name:     "slow",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 300},
		Payload:  Payload{Kind: PayloadMessage, Text: "slow"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	})
	require.NoError(t, err)
	fast, err := svc.Add(ctx, CreateJob{
		Name:     "fast",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "fast"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	})
	require.NoError(t, err)
	enabled := false
	_, err = svc.Add(ctx, CreateJob{
		Name:     "disabled",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 30},
		Payload:  Payload{Kind: PayloadMessage, Text: "disabled"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		Enabled:  &enabled,
	})
	require.NoError(t, err)

	active, err := svc.List(ctx, false)
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, fast.ID, active[0].ID)
	assert.Equal(t, slow.ID, active[1].ID)

	all, err := svc.List(ctx, true)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	byShort, ok, err := svc.Get(ctx, shortID(fast.ID))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, fast.ID, byShort.ID)

	byName, ok, err := svc.Get(ctx, "SLOW")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, slow.ID, byName.ID)

	status, err := svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, true, status["enabled"])
	assert.Equal(t, 3, status["jobs"])
	assert.Equal(t, now.Add(time.Minute).Format(time.RFC3339), status["next_run_at"])
}

func TestServiceStartMarksInterruptedRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	runningAt := now.Add(-time.Minute)
	require.NoError(t, saveJobs(path, []Job{{
		ID:       "cron_running",
		Name:     "running",
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60, Anchor: now.Format(time.RFC3339)},
		Payload:  Payload{Kind: PayloadMessage, Text: "hello"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		State:    JobState{RunningAt: &runningAt},
	}}))

	svc := New(Options{
		Enabled:   true,
		StorePath: path,
		Now:       func() time.Time { return now },
	})
	require.NoError(t, svc.Start(ctx))
	defer svc.Stop()

	got, ok, err := svc.Get(ctx, "cron_running")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, got.State.RunningAt)
	require.NotNil(t, got.State.LastRunAt)
	assert.True(t, got.State.LastRunAt.Equal(runningAt))
	assert.Equal(t, StatusError, got.State.LastStatus)
	assert.Contains(t, got.State.LastError, "interrupted")
}

func TestServiceTickRunsDueJobs(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	delivered := make(chan string, 1)
	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			delivered <- text
			return nil
		},
	})
	job, err := svc.Add(ctx, CreateJob{
		Name:     "due",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "run me"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	})
	require.NoError(t, err)

	past := now.Add(-time.Minute)
	svc.mu.Lock()
	svc.running = true
	svc.ctx = ctx
	svc.jobs[0].State.NextRunAt = &past
	svc.mu.Unlock()
	defer svc.Stop()

	svc.tick()
	assert.Equal(t, "run me", <-delivered)
	require.Eventually(t, func() bool {
		got, ok, err := svc.Get(ctx, job.ID)
		return err == nil && ok && got.State.RunCount == 1 && got.State.LastStatus == StatusOK
	}, time.Second, 10*time.Millisecond)
}

func TestServiceRunNowErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := New(Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
		Deliver:   func(ctx context.Context, target channel.DeliveryTarget, text string) error { return nil },
	})
	_, err := svc.RunNow(ctx, "missing")
	assert.ErrorContains(t, err, "unknown cron job id")

	enabled := false
	job, err := svc.Add(ctx, CreateJob{
		Name:     "disabled",
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "hello"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
		Enabled:  &enabled,
	})
	require.NoError(t, err)
	_, err = svc.RunNow(ctx, job.ID)
	assert.ErrorContains(t, err, "disabled")

	enabled = true
	_, err = svc.Update(ctx, job.ID, PatchJob{Enabled: &enabled})
	require.NoError(t, err)
	svc.mu.Lock()
	svc.jobs[0].State.RunningAt = &now
	svc.mu.Unlock()
	_, err = svc.RunNow(ctx, job.ID)
	assert.ErrorContains(t, err, "already running")
}

func TestServiceExecuteErrorPaths(t *testing.T) {
	ctx := context.Background()
	job := Job{
		ID:       "cron_x",
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	}

	svc := New(Options{Enabled: true})
	job.Payload = Payload{Kind: PayloadMessage, Text: "hello"}
	result := svc.execute(ctx, job)
	assert.Equal(t, StatusError, result.Status)
	assert.Contains(t, result.Error, "delivery is not configured")

	job.Payload = Payload{Kind: PayloadAgent, Text: "hello"}
	result = svc.execute(ctx, job)
	assert.Equal(t, StatusError, result.Status)
	assert.Contains(t, result.Error, "agent cron payloads are not configured")

	job.Payload = Payload{Kind: "other", Text: "hello"}
	result = svc.execute(ctx, job)
	assert.Equal(t, StatusError, result.Status)
	assert.Contains(t, result.Error, "unknown payload kind")

	svc = New(Options{
		Enabled: true,
		Deliver: func(ctx context.Context, target channel.DeliveryTarget, text string) error {
			return errors.New("send failed")
		},
	})
	job.Payload = Payload{Kind: PayloadMessage, Text: "hello"}
	result = svc.execute(ctx, job)
	assert.Equal(t, StatusError, result.Status)
	assert.Contains(t, result.Error, "send failed")

	job.Payload = Payload{Kind: PayloadMessage, Text: ""}
	result = svc.execute(ctx, job)
	assert.Equal(t, StatusOK, result.Status)
	assert.False(t, result.Delivered)
}

func TestServiceStartDisabled(t *testing.T) {
	svc := New(Options{
		Enabled:   false,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
	})
	require.NoError(t, svc.Start(context.Background()))
	svc.Stop()
}

func TestServiceRecomputeJobLockedCases(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc := New(Options{Enabled: true})

	disabled := Job{
		Enabled:  false,
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
	}
	require.NoError(t, svc.recomputeJobLocked(&disabled, now))
	assert.Nil(t, disabled.State.NextRunAt)

	pastAt := Job{
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleAt, At: now.Add(-time.Minute).Format(time.RFC3339)},
	}
	require.NoError(t, svc.recomputeJobLocked(&pastAt, now))
	assert.False(t, pastAt.Enabled)
	assert.Nil(t, pastAt.State.NextRunAt)
	assert.NotNil(t, pastAt.State.DisabledAt)

	invalid := Job{
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleEvery},
	}
	err := svc.recomputeJobLocked(&invalid, now)
	require.Error(t, err)
	assert.Nil(t, invalid.State.NextRunAt)
}

func TestValidateJobErrors(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	valid := Job{
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 60},
		Payload:  Payload{Kind: PayloadMessage, Text: "hello"},
		Delivery: channel.DeliveryTarget{Channel: "test", ChatID: 42},
	}

	tests := []struct {
		name string
		mut  func(*Job)
		want string
	}{
		{name: "schedule kind", mut: func(j *Job) { j.Schedule.Kind = "" }, want: "schedule.kind is required"},
		{name: "payload kind", mut: func(j *Job) { j.Payload.Kind = "" }, want: "payload.kind is required"},
		{name: "payload text", mut: func(j *Job) { j.Payload.Text = "" }, want: "payload.text is required"},
		{name: "delivery channel", mut: func(j *Job) { j.Delivery.Channel = "" }, want: "delivery.channel is required"},
		{name: "delivery target", mut: func(j *Job) { j.Delivery.ChatID = 0 }, want: "delivery target is required"},
		{name: "unsupported payload", mut: func(j *Job) { j.Payload.Kind = "webhook" }, want: "unsupported payload.kind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := valid
			tt.mut(&job)
			err := validateJob(job, now)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestServiceHelpers(t *testing.T) {
	assert.Equal(t, "", formatTimePtr(nil))
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, "2026-06-11T10:00:00Z", formatTimePtr(&now))
	assert.Equal(t, "short", shortID("short"))

	svc := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	svc.debug("test helper")
}
