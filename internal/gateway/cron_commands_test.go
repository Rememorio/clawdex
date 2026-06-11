package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
)

func TestSelectCronJob(t *testing.T) {
	jobs := []cronjob.Job{
		{ID: "cron_1111111111111111", Name: "daily"},
		{ID: "cron_2222222222222222", Name: "weekly"},
	}

	job, err := selectCronJob(jobs, "2")
	require.NoError(t, err)
	assert.Equal(t, "weekly", job.Name)

	job, err = selectCronJob(jobs, "cron_11111111")
	require.NoError(t, err)
	assert.Equal(t, "daily", job.Name)

	job, err = selectCronJob(jobs, "WEEKLY")
	require.NoError(t, err)
	assert.Equal(t, "weekly", job.Name)

	_, err = selectCronJob(jobs, "3")
	assert.ErrorContains(t, err, "out of range")
}

func TestFormatCronListAndDetails(t *testing.T) {
	next := time.Date(2026, 6, 11, 10, 5, 0, 0, time.UTC)
	job := cronjob.Job{
		ID:       "cron_1111111111111111",
		Name:     "daily",
		Enabled:  true,
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleEvery, EverySeconds: 300},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: "hello"},
		State: cronjob.JobState{
			NextRunAt:   &next,
			LastStatus:  cronjob.StatusOK,
			RunCount:    2,
			LastRunAt:   &next,
			LastError:   "",
			RunningAt:   nil,
			Delivered:   true,
			DisabledAt:  nil,
			CompletedAt: nil,
		},
	}

	list := formatCronList([]cronjob.Job{job})
	assert.Contains(t, list, "1. daily `cron_11111111` enabled")
	assert.Contains(t, list, "next=2026-06-11 10:05:00 UTC")

	details := formatCronDetails(job)
	assert.Contains(t, details, "Scheduled job: daily")
	assert.Contains(t, details, "Schedule: every 300s")
	assert.Contains(t, details, "Runs: 2")
}

func TestHandleCronCommandListAndStop(t *testing.T) {
	svc := New(nil, 1, "off")
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	svc.SetCron(cronjob.New(cronjob.Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
	}))

	target := channel.DeliveryTarget{Channel: "telegram", ChatID: 42, Target: "42"}
	job, err := svc.cron.Add(context.Background(), cronjob.CreateJob{
		Name:     "daily",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleEvery, EverySeconds: 60},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: "hello"},
		Delivery: target,
		ScopeID:  42,
	})
	require.NoError(t, err)

	msg := channel.Message{Channel: "telegram", ChatID: 42, Target: "42", Text: "/cron list"}
	resp, ok := svc.handleCronCommand(context.Background(), msg)
	require.True(t, ok)
	assert.Contains(t, resp.text, "daily")

	msg.Text = "/cron stop daily"
	resp, ok = svc.handleCronCommand(context.Background(), msg)
	require.True(t, ok)
	assert.Contains(t, resp.text, "Stopped scheduled job: daily")

	updated, ok, err := svc.cron.Get(context.Background(), job.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, updated.Enabled)
}
