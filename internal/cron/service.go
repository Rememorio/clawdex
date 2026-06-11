package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
)

type DeliverFunc func(context.Context, channel.DeliveryTarget, string) error
type AgentFunc func(context.Context, Job) (string, error)

type Options struct {
	StorePath string
	Enabled   bool
	Deliver   DeliverFunc
	RunAgent  AgentFunc
	Now       func() time.Time
	Log       *slog.Logger
}

type Service struct {
	path     string
	enabled  bool
	deliver  DeliverFunc
	runAgent AgentFunc
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	loaded  bool
	jobs    []Job
	timer   *time.Timer
	running bool
	ctx     context.Context
}

func New(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		path:     opts.StorePath,
		enabled:  opts.Enabled,
		deliver:  opts.Deliver,
		runAgent: opts.RunAgent,
		now:      now,
		log:      opts.Log,
	}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		s.debug("cron disabled")
		return nil
	}
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	now := s.now()
	changed := false
	for i := range s.jobs {
		if s.jobs[i].State.RunningAt != nil {
			s.jobs[i].State.LastRunAt = s.jobs[i].State.RunningAt
			s.jobs[i].State.RunningAt = nil
			s.jobs[i].State.LastStatus = StatusError
			s.jobs[i].State.LastError = "cron: interrupted by gateway restart"
			changed = true
		}
		if err := s.recomputeLocked(i, now); err != nil {
			s.jobs[i].State.LastStatus = StatusError
			s.jobs[i].State.LastError = err.Error()
			changed = true
		}
	}
	if changed {
		if err := s.persistLocked(); err != nil {
			return err
		}
	}
	s.running = true
	s.ctx = ctx
	s.armLocked()
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *Service) Status(ctx context.Context) (map[string]any, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	return map[string]any{
		"enabled":     s.enabled,
		"store_path":  s.path,
		"jobs":        len(s.jobs),
		"next_run_at": formatTimePtr(s.nextRunLocked()),
	}, nil
}

func (s *Service) List(ctx context.Context, includeDisabled bool) ([]Job, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		if includeDisabled || job.Enabled {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i].State.NextRunAt
		b := out[j].State.NextRunAt
		switch {
		case a == nil && b == nil:
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		case a == nil:
			return false
		case b == nil:
			return true
		default:
			return a.Before(*b)
		}
	})
	return out, nil
}

func (s *Service) Get(ctx context.Context, id string) (Job, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Job{}, false, err
	}
	idx := s.findLocked(id)
	if idx < 0 {
		return Job{}, false, nil
	}
	return s.jobs[idx], true, nil
}

func (s *Service) Add(ctx context.Context, input CreateJob) (Job, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Job{}, err
	}
	now := s.now().UTC()
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	job := Job{
		ID:        newID(),
		Name:      strings.TrimSpace(input.Name),
		Enabled:   enabled,
		Schedule:  normalizeSchedule(input.Schedule, now),
		Payload:   normalizePayload(input.Payload),
		Delivery:  input.Delivery,
		ScopeID:   input.ScopeID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := validateJob(job, now); err != nil {
		return Job{}, err
	}
	if enabled {
		if err := s.recomputeJobLocked(&job, now); err != nil {
			return Job{}, err
		}
		if job.State.NextRunAt == nil {
			return Job{}, fmt.Errorf("schedule has no upcoming run time")
		}
	}
	s.jobs = append(s.jobs, job)
	if err := s.persistLocked(); err != nil {
		return Job{}, err
	}
	s.armLocked()
	return job, nil
}

func (s *Service) Update(ctx context.Context, id string, patch PatchJob) (Job, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Job{}, err
	}
	idx := s.findLocked(id)
	if idx < 0 {
		return Job{}, fmt.Errorf("unknown cron job id: %s", id)
	}
	now := s.now().UTC()
	job := s.jobs[idx]
	if patch.Name != nil {
		job.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Enabled != nil {
		job.Enabled = *patch.Enabled
		if !job.Enabled {
			job.State.NextRunAt = nil
			job.State.DisabledAt = &now
		} else {
			job.State.DisabledAt = nil
		}
	}
	if patch.Schedule != nil {
		job.Schedule = normalizeSchedule(*patch.Schedule, now)
	}
	if patch.Payload != nil {
		job.Payload = normalizePayload(*patch.Payload)
	}
	job.UpdatedAt = now
	if err := validateJob(job, now); err != nil {
		return Job{}, err
	}
	if job.Enabled {
		if err := s.recomputeJobLocked(&job, now); err != nil {
			return Job{}, err
		}
		if job.State.NextRunAt == nil {
			return Job{}, fmt.Errorf("schedule has no upcoming run time")
		}
	}
	s.jobs[idx] = job
	if err := s.persistLocked(); err != nil {
		return Job{}, err
	}
	s.armLocked()
	return job, nil
}

func (s *Service) Remove(ctx context.Context, id string) (bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return false, err
	}
	idx := s.findLocked(id)
	if idx < 0 {
		return false, nil
	}
	s.jobs = append(s.jobs[:idx], s.jobs[idx+1:]...)
	if err := s.persistLocked(); err != nil {
		return false, err
	}
	s.armLocked()
	return true, nil
}

func (s *Service) RunNow(ctx context.Context, id string) (RunResult, error) {
	s.mu.Lock()
	if err := s.ensureLoadedLocked(); err != nil {
		s.mu.Unlock()
		return RunResult{}, err
	}
	idx := s.findLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return RunResult{}, fmt.Errorf("unknown cron job id: %s", id)
	}
	job := s.jobs[idx]
	if !job.Enabled {
		s.mu.Unlock()
		return RunResult{}, fmt.Errorf("cron job is disabled")
	}
	if job.State.RunningAt != nil {
		s.mu.Unlock()
		return RunResult{}, fmt.Errorf("cron job is already running")
	}
	runAt := s.now().UTC()
	s.jobs[idx].State.RunningAt = &runAt
	s.jobs[idx].State.LastStatus = StatusRunning
	s.jobs[idx].UpdatedAt = runAt
	_ = s.persistLocked()
	s.mu.Unlock()

	result := s.execute(ctx, job)
	s.finishRun(id, runAt, result)
	return result, nil
}

func (s *Service) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	jobs, err := loadJobs(s.path)
	if err != nil {
		return err
	}
	s.jobs = jobs
	s.loaded = true
	return nil
}

func (s *Service) persistLocked() error {
	return saveJobs(s.path, s.jobs)
}

func (s *Service) findLocked(id string) int {
	id = strings.TrimSpace(id)
	for i := range s.jobs {
		if s.jobs[i].ID == id || shortID(s.jobs[i].ID) == id {
			return i
		}
	}
	for i := range s.jobs {
		if strings.EqualFold(s.jobs[i].Name, id) && s.jobs[i].Name != "" {
			return i
		}
	}
	return -1
}

func (s *Service) recomputeLocked(idx int, now time.Time) error {
	return s.recomputeJobLocked(&s.jobs[idx], now)
}

func (s *Service) recomputeJobLocked(job *Job, now time.Time) error {
	if !job.Enabled {
		job.State.NextRunAt = nil
		return nil
	}
	next, err := computeNextRun(job.Schedule, now)
	if err != nil {
		job.State.NextRunAt = nil
		return err
	}
	job.State.NextRunAt = next
	if next == nil && job.Schedule.Kind == ScheduleAt {
		job.Enabled = false
		disabledAt := now.UTC()
		job.State.DisabledAt = &disabledAt
	}
	return nil
}

func (s *Service) nextRunLocked() *time.Time {
	var next *time.Time
	for i := range s.jobs {
		jobNext := s.jobs[i].State.NextRunAt
		if !s.jobs[i].Enabled || jobNext == nil || s.jobs[i].State.RunningAt != nil {
			continue
		}
		if next == nil || jobNext.Before(*next) {
			copy := *jobNext
			next = &copy
		}
	}
	return next
}

func (s *Service) armLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if !s.running || !s.enabled {
		return
	}
	next := s.nextRunLocked()
	if next == nil {
		return
	}
	delay := time.Until(*next)
	if delay < 0 {
		delay = 0
	}
	s.timer = time.AfterFunc(delay, s.tick)
}

func (s *Service) tick() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	ctx := s.ctx
	now := s.now().UTC()
	var due []Job
	for i := range s.jobs {
		job := &s.jobs[i]
		if !job.Enabled || job.State.NextRunAt == nil || job.State.RunningAt != nil {
			continue
		}
		if job.State.NextRunAt.After(now) {
			continue
		}
		job.State.RunningAt = &now
		job.State.LastStatus = StatusRunning
		job.UpdatedAt = now
		due = append(due, *job)
	}
	_ = s.persistLocked()
	s.armLocked()
	s.mu.Unlock()

	for _, job := range due {
		go func(job Job) {
			result := s.execute(ctx, job)
			s.finishRun(job.ID, now, result)
		}(job)
	}
}

func (s *Service) execute(ctx context.Context, job Job) RunResult {
	var text string
	var err error
	switch job.Payload.Kind {
	case PayloadMessage:
		text = job.Payload.Text
	case PayloadAgent:
		if s.runAgent == nil {
			err = fmt.Errorf("agent cron payloads are not configured")
		} else {
			text, err = s.runAgent(ctx, job)
		}
	default:
		err = fmt.Errorf("unknown payload kind %q", job.Payload.Kind)
	}
	if errors.Is(err, ErrAlreadyDelivered) {
		return RunResult{JobID: job.ID, Status: StatusOK, Delivered: true}
	}
	if err != nil {
		result := RunResult{JobID: job.ID, Status: StatusError, Error: err.Error()}
		if s.deliver != nil {
			if deliverErr := s.deliver(ctx, job.Delivery, formatFailureNotice(job, err)); deliverErr == nil {
				result.Delivered = true
			} else {
				result.Error = err.Error() + "; failure notice delivery failed: " + deliverErr.Error()
			}
		}
		return result
	}
	delivered := false
	if strings.TrimSpace(text) != "" {
		if s.deliver == nil {
			return RunResult{JobID: job.ID, Status: StatusError, Error: "cron delivery is not configured"}
		}
		if err := s.deliver(ctx, job.Delivery, text); err != nil {
			return RunResult{JobID: job.ID, Status: StatusError, Error: err.Error()}
		}
		delivered = true
	}
	return RunResult{JobID: job.ID, Status: StatusOK, Delivered: delivered}
}

func formatFailureNotice(job Job, err error) string {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		name = strings.TrimSpace(job.ID)
	}
	if name == "" {
		name = "scheduled job"
	}
	return "Scheduled job failed: " + name + "\nError: " + err.Error()
}

func (s *Service) finishRun(id string, started time.Time, result RunResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findLocked(id)
	if idx < 0 {
		return
	}
	now := s.now().UTC()
	job := &s.jobs[idx]
	job.State.RunningAt = nil
	job.State.LastRunAt = &started
	job.State.LastStatus = result.Status
	job.State.LastError = result.Error
	job.State.Delivered = result.Delivered
	job.State.RunCount++
	job.UpdatedAt = now
	if job.Schedule.Kind == ScheduleAt {
		job.Enabled = false
		job.State.CompletedAt = &now
		job.State.NextRunAt = nil
	} else if job.Enabled {
		if err := s.recomputeJobLocked(job, now); err != nil {
			job.State.LastStatus = StatusError
			job.State.LastError = err.Error()
			job.State.NextRunAt = nil
		}
	}
	_ = s.persistLocked()
	s.armLocked()
}

func validateJob(job Job, now time.Time) error {
	if strings.TrimSpace(job.Schedule.Kind) == "" {
		return fmt.Errorf("schedule.kind is required")
	}
	if strings.TrimSpace(job.Payload.Kind) == "" {
		return fmt.Errorf("payload.kind is required")
	}
	if strings.TrimSpace(job.Payload.Text) == "" {
		return fmt.Errorf("payload.text is required")
	}
	if strings.TrimSpace(job.Delivery.Channel) == "" {
		return fmt.Errorf("delivery.channel is required")
	}
	if job.Delivery.ChatID == 0 && strings.TrimSpace(job.Delivery.Target) == "" {
		return fmt.Errorf("delivery target is required")
	}
	if _, err := computeNextRun(job.Schedule, now.UTC()); err != nil {
		return err
	}
	switch job.Payload.Kind {
	case PayloadMessage, PayloadAgent:
		return nil
	default:
		return fmt.Errorf("unsupported payload.kind %q", job.Payload.Kind)
	}
}

func normalizeSchedule(schedule Schedule, now time.Time) Schedule {
	schedule.Kind = strings.ToLower(strings.TrimSpace(schedule.Kind))
	schedule.At = strings.TrimSpace(schedule.At)
	schedule.Anchor = strings.TrimSpace(schedule.Anchor)
	schedule.Expr = strings.TrimSpace(schedule.Expr)
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	if schedule.Kind == ScheduleEvery && schedule.Anchor == "" {
		schedule.Anchor = now.UTC().Format(time.RFC3339)
	}
	return schedule
}

func normalizePayload(payload Payload) Payload {
	payload.Kind = strings.ToLower(strings.TrimSpace(payload.Kind))
	payload.Text = strings.TrimSpace(payload.Text)
	return payload
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func shortID(id string) string {
	if len(id) <= 13 {
		return id
	}
	return id[:13]
}

func (s *Service) debug(msg string, args ...any) {
	if s.log != nil {
		s.log.Debug(msg, args...)
	}
}
