package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeNextRunAt(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind: ScheduleAt,
		At:   now.Add(time.Hour).Format(time.RFC3339),
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, now.Add(time.Hour), *next)

	next, err = computeNextRun(Schedule{
		Kind: ScheduleAt,
		At:   now.Add(-time.Hour).Format(time.RFC3339),
	}, now)
	require.NoError(t, err)
	assert.Nil(t, next)
}

func TestComputeNextRunEvery(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 3, 10, 0, time.UTC)
	anchor := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind:         ScheduleEvery,
		EverySeconds: 300,
		Anchor:       anchor.Format(time.RFC3339),
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, time.Date(2026, 6, 11, 10, 5, 0, 0, time.UTC), *next)
}

func TestComputeNextRunEveryBeforeAnchor(t *testing.T) {
	now := time.Date(2026, 6, 11, 9, 55, 0, 0, time.UTC)
	anchor := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind:         ScheduleEvery,
		EverySeconds: 60,
		Anchor:       anchor.Format(time.RFC3339),
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, anchor, *next)
}

func TestComputeNextRunCronUTC(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 58, 20, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind:     ScheduleCron,
		Expr:     "0 11 * * *",
		Timezone: "UTC",
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC), *next)
}

func TestComputeNextRunCronTimezone(t *testing.T) {
	now := time.Date(2026, 6, 11, 0, 50, 0, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind:     ScheduleCron,
		Expr:     "0 9 * * *",
		Timezone: "Asia/Shanghai",
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, time.Date(2026, 6, 11, 1, 0, 0, 0, time.UTC), *next)
}

func TestComputeNextRunCronNormalizesSundaySeven(t *testing.T) {
	now := time.Date(2026, 6, 13, 23, 55, 0, 0, time.UTC)

	next, err := computeNextRun(Schedule{
		Kind:     ScheduleCron,
		Expr:     "0 0 * * 7",
		Timezone: "UTC",
	}, now)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC), *next)
}

func TestComputeNextRunRejectsInvalidSchedules(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule Schedule
		wantErr  string
	}{
		{name: "unknown kind", schedule: Schedule{Kind: "later"}, wantErr: "unknown schedule kind"},
		{name: "bad at", schedule: Schedule{Kind: ScheduleAt, At: "tomorrow"}, wantErr: "invalid at timestamp"},
		{name: "bad every", schedule: Schedule{Kind: ScheduleEvery}, wantErr: "every_seconds must be positive"},
		{name: "bad cron fields", schedule: Schedule{Kind: ScheduleCron, Expr: "* * *"}, wantErr: "cron expr must have 5 fields"},
		{name: "bad cron range", schedule: Schedule{Kind: ScheduleCron, Expr: "60 * * * *"}, wantErr: "value 60 out of range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := computeNextRun(tt.schedule, now)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParseCronFieldRangesAndSteps(t *testing.T) {
	values, err := parseCronField("1-5/2,9", 0, 10, false)
	require.NoError(t, err)
	assert.Equal(t, map[int]bool{1: true, 3: true, 5: true, 9: true}, values)

	values, err = parseCronField("7", 0, 7, true)
	require.NoError(t, err)
	assert.Equal(t, map[int]bool{0: true}, values)
}

func TestParseCronFieldRejectsInvalidParts(t *testing.T) {
	tests := []string{
		"",
		"*/0",
		"5-1",
		"x",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := parseCronField(raw, 0, 10, false)
			assert.Error(t, err)
		})
	}
}
