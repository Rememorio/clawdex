package cron

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
)

func TestJobJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	job := Job{
		ID:       "cron_1234",
		Name:     "reminder",
		Enabled:  true,
		Schedule: Schedule{Kind: ScheduleEvery, EverySeconds: 600},
		Payload:  Payload{Kind: PayloadMessage, Text: "stretch"},
		Delivery: channel.DeliveryTarget{Channel: "telegram", ChatID: 42, Target: "42"},
		ScopeID:  42,
		State: JobState{
			NextRunAt:  &now,
			LastStatus: StatusOK,
			RunCount:   2,
			Delivered:  true,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(job)
	require.NoError(t, err)

	var loaded Job
	require.NoError(t, json.Unmarshal(data, &loaded))
	assert.Equal(t, job.ID, loaded.ID)
	assert.Equal(t, job.Schedule.EverySeconds, loaded.Schedule.EverySeconds)
	assert.Equal(t, job.Payload.Text, loaded.Payload.Text)
	assert.Equal(t, job.Delivery.Target, loaded.Delivery.Target)
	assert.Equal(t, job.State.RunCount, loaded.State.RunCount)
}
