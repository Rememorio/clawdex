package cron

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
)

func TestLoadJobsMissingFile(t *testing.T) {
	jobs, err := loadJobs(filepath.Join(t.TempDir(), "missing", "jobs.json"))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestSaveAndLoadJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron", "jobs.json")
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	input := []Job{{
		ID:       "cron_test",
		Name:     "daily",
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleCron, Expr: "0 9 * * *", Timezone: "UTC"},
		Payload:  Payload{Kind: PayloadMessage, Text: "standup"},
		Delivery: channel.DeliveryTarget{Channel: "telegram", ChatID: 42, Target: "42"},
		ScopeID:  42,
		State: JobState{
			NextRunAt:  &now,
			LastStatus: StatusOK,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}

	require.NoError(t, saveJobs(path, input))
	loaded, err := loadJobs(path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, input[0].ID, loaded[0].ID)
	assert.Equal(t, input[0].Delivery.Target, loaded[0].Delivery.Target)
	require.NotNil(t, loaded[0].State.NextRunAt)
	assert.True(t, loaded[0].State.NextRunAt.Equal(now))
}

func TestLoadJobsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	require.NoError(t, os.WriteFile(path, []byte("{bad"), 0o644))

	_, err := loadJobs(path)
	assert.Error(t, err)
}

func TestSaveJobsRenameError(t *testing.T) {
	dir := t.TempDir()

	err := saveJobs(dir, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rename cron store")
}
