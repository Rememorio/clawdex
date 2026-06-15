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

func TestParseNaturalCronRequestRelativeReminder(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 3, 38, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "5 分钟后提醒我去喝水@Ardelia(艾尔黛拉)")

	require.NoError(t, err)
	assert.True(t, intent)
	assert.Equal(t, "reminder", req.Name)
	assert.Equal(t, cronjob.ScheduleAt, req.Schedule.Kind)
	assert.Equal(t, now.Add(5*time.Minute).Format(time.RFC3339), req.Schedule.At)
	assert.Equal(t, cronjob.PayloadMessage, req.Payload.Kind)
	assert.Equal(t, "去喝水", req.Payload.Text)
}

func TestParseNaturalCronRequestClockReminder(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "12:00提醒我午休")

	require.NoError(t, err)
	assert.True(t, intent)
	assert.Equal(t, "2026-06-12T12:00:00+08:00", req.Schedule.At)
	assert.Equal(t, "午休", req.Payload.Text)

	req, intent, err = parseNaturalCronRequest(now.Add(2*time.Hour), "12:00提醒我午休")

	require.NoError(t, err)
	assert.True(t, intent)
	assert.Equal(t, "2026-06-13T12:00:00+08:00", req.Schedule.At)
}

func TestParseNaturalCronRequestDailyTask(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "请帮我每天早上 10点执行以下定时任务 做报告")

	require.NoError(t, err)
	assert.True(t, intent)
	assert.Equal(t, "daily_task", req.Name)
	assert.Equal(t, cronjob.ScheduleCron, req.Schedule.Kind)
	assert.Equal(t, "0 10 * * *", req.Schedule.Expr)
	assert.Equal(t, cronjob.PayloadAgent, req.Payload.Kind)
	assert.Equal(t, "做报告", req.Payload.Text)
}

func TestParseNaturalCronRequestDailyTaskAfterMarker(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "请帮我每日10点的定时任务：做报告")

	require.NoError(t, err)
	assert.True(t, intent)
	assert.Equal(t, "0 10 * * *", req.Schedule.Expr)
	assert.Equal(t, cronjob.PayloadAgent, req.Payload.Kind)
	assert.Equal(t, "做报告", req.Payload.Text)
}

func TestParseNaturalCronRequestContextualDailyTaskDefersToAgent(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "可以，你按这个创建每日10 点的定时任务，然后现在触发一次")

	require.NoError(t, err)
	assert.False(t, intent)
	assert.Empty(t, req.Payload.Text)
}

func TestParseNaturalCronRequestImmediateRunDefersToAgent(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, loc)

	req, intent, err := parseNaturalCronRequest(now, "每天10点执行做报告，然后现在触发一次")

	require.NoError(t, err)
	assert.False(t, intent)
	assert.Empty(t, req.Payload.Text)
}

func TestParseNaturalCronRequestUnsupportedIntent(t *testing.T) {
	now := time.Date(2026, 6, 12, 11, 0, 0, 0, time.Local)

	_, intent, err := parseNaturalCronRequest(now, "下个月 1 号提醒我交房租")

	assert.True(t, intent)
	assert.ErrorContains(t, err, "unsupported schedule format")
}

func TestNaturalReminderCreatesCronWithoutCodex(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 12, 11, 3, 38, 0, loc)
	oldNow := naturalCronNow
	naturalCronNow = func() time.Time { return now }
	defer func() { naturalCronNow = oldNow }()

	svc := New(nil, 1, "partial")
	cronSvc := cronjob.New(cronjob.Options{
		Enabled:   true,
		StorePath: filepath.Join(t.TempDir(), "jobs.json"),
		Now:       func() time.Time { return now },
	})
	svc.SetCron(cronSvc)
	resp := &mockResponder{}
	msg := channel.Message{
		Channel:   "wecom",
		ChatID:    42,
		Target:    "group-42",
		ChatType:  "group",
		SenderID:  7,
		MessageID: 100,
		Text:      "5 分钟后提醒我去喝水@Ardelia(艾尔黛拉)",
	}

	svc.processJob(context.Background(), job{msg: msg, responder: resp})

	resp.mu.Lock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Scheduled job created.")
	assert.Empty(t, resp.typings)
	resp.mu.Unlock()

	jobs, err := cronSvc.List(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "wecom", jobs[0].Delivery.Channel)
	assert.Equal(t, int64(42), jobs[0].Delivery.ChatID)
	assert.Equal(t, cronjob.PayloadMessage, jobs[0].Payload.Kind)
	assert.Equal(t, "去喝水", jobs[0].Payload.Text)
	require.NotNil(t, jobs[0].State.NextRunAt)
	assert.Equal(t, now.Add(5*time.Minute).UTC(), jobs[0].State.NextRunAt.UTC())
}
