package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const cronSearchLimit = 5 * 366 * 24 * time.Hour

func computeNextRun(schedule Schedule, now time.Time) (*time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(schedule.Kind)) {
	case ScheduleAt:
		return computeAtNext(schedule, now)
	case ScheduleEvery:
		return computeEveryNext(schedule, now)
	case ScheduleCron:
		return computeCronNext(schedule, now)
	default:
		return nil, fmt.Errorf("unknown schedule kind %q", schedule.Kind)
	}
}

func computeAtNext(schedule Schedule, now time.Time) (*time.Time, error) {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(schedule.At))
	if err != nil {
		return nil, fmt.Errorf("invalid at timestamp: %w", err)
	}
	at = at.UTC()
	if !at.After(now.UTC()) {
		return nil, nil
	}
	return &at, nil
}

func computeEveryNext(schedule Schedule, now time.Time) (*time.Time, error) {
	if schedule.EverySeconds <= 0 {
		return nil, fmt.Errorf("every_seconds must be positive")
	}
	interval := time.Duration(schedule.EverySeconds) * time.Second
	anchor := now.UTC()
	if strings.TrimSpace(schedule.Anchor) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(schedule.Anchor))
		if err != nil {
			return nil, fmt.Errorf("invalid every anchor: %w", err)
		}
		anchor = parsed.UTC()
	}
	now = now.UTC()
	if now.Before(anchor) {
		return &anchor, nil
	}
	elapsed := now.Sub(anchor)
	steps := elapsed/interval + 1
	next := anchor.Add(steps * interval)
	return &next, nil
}

func computeCronNext(schedule Schedule, now time.Time) (*time.Time, error) {
	expr := strings.TrimSpace(schedule.Expr)
	if expr == "" {
		return nil, fmt.Errorf("cron expr is required")
	}
	spec, err := parseCronSpec(expr)
	if err != nil {
		return nil, err
	}
	loc := time.Local
	if tz := strings.TrimSpace(schedule.Timezone); tz != "" {
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", tz, err)
		}
	}
	start := now.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := start.Add(cronSearchLimit)
	for t := start; !t.After(deadline); t = t.Add(time.Minute) {
		if spec.matches(t) {
			utc := t.UTC()
			return &utc, nil
		}
	}
	return nil, fmt.Errorf("cron schedule has no run within search window")
}

type cronSpec struct {
	minutes  map[int]bool
	hours    map[int]bool
	days     map[int]bool
	months   map[int]bool
	weekdays map[int]bool
}

func (s cronSpec) matches(t time.Time) bool {
	return s.minutes[t.Minute()] &&
		s.hours[t.Hour()] &&
		s.days[t.Day()] &&
		s.months[int(t.Month())] &&
		s.weekdays[int(t.Weekday())]
}

func parseCronSpec(expr string) (cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSpec{}, fmt.Errorf("cron expr must have 5 fields")
	}
	minutes, err := parseCronField(fields[0], 0, 59, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("minute field: %w", err)
	}
	hours, err := parseCronField(fields[1], 0, 23, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("hour field: %w", err)
	}
	days, err := parseCronField(fields[2], 1, 31, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("day field: %w", err)
	}
	months, err := parseCronField(fields[3], 1, 12, false)
	if err != nil {
		return cronSpec{}, fmt.Errorf("month field: %w", err)
	}
	weekdays, err := parseCronField(fields[4], 0, 7, true)
	if err != nil {
		return cronSpec{}, fmt.Errorf("weekday field: %w", err)
	}
	return cronSpec{
		minutes:  minutes,
		hours:    hours,
		days:     days,
		months:   months,
		weekdays: weekdays,
	}, nil
}

func parseCronField(raw string, min, max int, normalizeSunday bool) (map[int]bool, error) {
	out := make(map[int]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty field part")
		}
		step := 1
		if base, stepRaw, ok := strings.Cut(part, "/"); ok {
			part = base
			parsed, err := strconv.Atoi(stepRaw)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("invalid step %q", stepRaw)
			}
			step = parsed
		}
		start, end, err := parseCronRange(part, min, max)
		if err != nil {
			return nil, err
		}
		for value := start; value <= end; value += step {
			v := value
			if normalizeSunday && v == 7 {
				v = 0
			}
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("field selects no values")
	}
	return out, nil
}

func parseCronRange(raw string, min, max int) (int, int, error) {
	if raw == "*" {
		return min, max, nil
	}
	if startRaw, endRaw, ok := strings.Cut(raw, "-"); ok {
		start, err := parseCronValue(startRaw, min, max)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseCronValue(endRaw, min, max)
		if err != nil {
			return 0, 0, err
		}
		if end < start {
			return 0, 0, fmt.Errorf("range %q ends before it starts", raw)
		}
		return start, end, nil
	}
	value, err := parseCronValue(raw, min, max)
	if err != nil {
		return 0, 0, err
	}
	return value, value, nil
}

func parseCronValue(raw string, min, max int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", raw)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("value %d out of range %d-%d", value, min, max)
	}
	return value, nil
}
