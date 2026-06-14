package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
	"github.com/Rememorio/clawdex/internal/logger"
	"github.com/Rememorio/clawdex/internal/server"
)

const cronContextTTL = 2 * time.Hour
const cronAgentMessageDelimiter = "<<<CLAWDEX_MESSAGE>>>"

type cronToolRequest struct {
	Token           string           `json:"token"`
	Action          string           `json:"action"`
	IncludeDisabled bool             `json:"include_disabled,omitempty"`
	Job             cronToolJob      `json:"job,omitempty"`
	ID              string           `json:"id,omitempty"`
	JobID           string           `json:"job_id,omitempty"`
	Patch           cronToolPatch    `json:"patch,omitempty"`
	Text            string           `json:"text,omitempty"`
	Schedule        cronjob.Schedule `json:"schedule,omitempty"`
	Payload         cronjob.Payload  `json:"payload,omitempty"`
	Name            string           `json:"name,omitempty"`
	Enabled         *bool            `json:"enabled,omitempty"`
}

type cronToolJob struct {
	Name     string           `json:"name,omitempty"`
	Schedule cronjob.Schedule `json:"schedule"`
	Payload  cronjob.Payload  `json:"payload"`
	Enabled  *bool            `json:"enabled,omitempty"`
}

type cronToolPatch struct {
	Name     *string           `json:"name,omitempty"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Schedule *cronjob.Schedule `json:"schedule,omitempty"`
	Payload  *cronjob.Payload  `json:"payload,omitempty"`
}

func deliveryTargetFromMessage(msg channel.Message) channel.DeliveryTarget {
	return channel.DeliveryTarget{
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		ThreadID:   msg.ThreadID,
		ChatType:   msg.ChatType,
		Target:     msg.Target,
		SenderID:   msg.SenderID,
		SenderName: msg.SenderName,
	}
}

func (s *Service) newCronContext(msg channel.Message) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	token := hex.EncodeToString(b[:])
	now := time.Now().UTC()
	s.cronContexts.Range(func(key, value any) bool {
		ctx, ok := value.(cronContext)
		if ok && now.After(ctx.ExpiresAt) {
			s.cronContexts.Delete(key)
		}
		return true
	})
	s.cronContexts.Store(token, cronContext{
		Msg:       msg,
		Delivery:  deliveryTargetFromMessage(msg),
		ScopeID:   sessionScopeID(msg),
		ExpiresAt: now.Add(cronContextTTL),
	})
	return token
}

func (s *Service) lookupCronContext(token string) (cronContext, bool) {
	value, ok := s.cronContexts.Load(strings.TrimSpace(token))
	if !ok {
		return cronContext{}, false
	}
	ctx, ok := value.(cronContext)
	if !ok || time.Now().UTC().After(ctx.ExpiresAt) {
		s.cronContexts.Delete(token)
		return cronContext{}, false
	}
	return ctx, true
}

func (s *Service) CronRoutes() []server.RouteHandler {
	return []server.RouteHandler{{
		Pattern: "/cron/tool",
		Handler: s.handleCronTool,
	}}
}

func (s *Service) handleCronTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cron == nil {
		writeCronToolError(w, http.StatusServiceUnavailable, "cron is not configured")
		return
	}
	var req cronToolRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeCronToolError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeCronToolError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	ctx, ok := s.lookupCronContext(req.Token)
	if !ok {
		writeCronToolError(w, http.StatusUnauthorized, "invalid or expired cron context token")
		return
	}
	result, err := s.dispatchCronTool(r.Context(), ctx, req)
	if err != nil {
		writeCronToolError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCronToolJSON(w, result)
}

func (s *Service) dispatchCronTool(ctx context.Context, cronCtx cronContext, req cronToolRequest) (any, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "status":
		return s.cron.Status(ctx)
	case "list":
		jobs, err := s.cron.List(ctx, req.IncludeDisabled)
		if err != nil {
			return nil, err
		}
		return map[string]any{"jobs": filterCronJobsForDelivery(jobs, cronCtx.Delivery)}, nil
	case "get":
		id := cronRequestID(req)
		job, err := s.requireCronJobForDelivery(ctx, id, cronCtx.Delivery)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": job}, nil
	case "add":
		input := cronjob.CreateJob{
			Name:     firstNonEmpty(req.Job.Name, req.Name),
			Schedule: req.Job.Schedule,
			Payload:  req.Job.Payload,
			Delivery: cronCtx.Delivery,
			ScopeID:  cronCtx.ScopeID,
			Enabled:  req.Job.Enabled,
		}
		if input.Schedule.Kind == "" {
			input.Schedule = req.Schedule
		}
		if input.Payload.Kind == "" && req.Text != "" {
			input.Payload = cronjob.Payload{Kind: cronjob.PayloadMessage, Text: req.Text}
		} else if input.Payload.Kind == "" {
			input.Payload = req.Payload
		}
		if input.Enabled == nil {
			input.Enabled = req.Enabled
		}
		job, err := s.cron.Add(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": job}, nil
	case "update":
		id := cronRequestID(req)
		if _, err := s.requireCronJobForDelivery(ctx, id, cronCtx.Delivery); err != nil {
			return nil, err
		}
		job, err := s.cron.Update(ctx, id, cronjob.PatchJob{
			Name:     req.Patch.Name,
			Enabled:  req.Patch.Enabled,
			Schedule: req.Patch.Schedule,
			Payload:  req.Patch.Payload,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": job}, nil
	case "remove":
		id := cronRequestID(req)
		if _, err := s.requireCronJobForDelivery(ctx, id, cronCtx.Delivery); err != nil {
			return nil, err
		}
		removed, err := s.cron.Remove(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"removed": removed}, nil
	case "run":
		id := cronRequestID(req)
		if _, err := s.requireCronJobForDelivery(ctx, id, cronCtx.Delivery); err != nil {
			return nil, err
		}
		result, err := s.cron.StartNow(ctx, id)
		if err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported cron action %q", req.Action)
	}
}

func (s *Service) requireCronJobForDelivery(ctx context.Context, id string, delivery channel.DeliveryTarget) (cronjob.Job, error) {
	if strings.TrimSpace(id) == "" {
		return cronjob.Job{}, fmt.Errorf("job id is required")
	}
	job, ok, err := s.cron.Get(ctx, id)
	if err != nil {
		return cronjob.Job{}, err
	}
	if !ok {
		return cronjob.Job{}, fmt.Errorf("unknown cron job id: %s", id)
	}
	if !sameCronDelivery(job.Delivery, delivery) {
		return cronjob.Job{}, fmt.Errorf("cron job is outside the current chat")
	}
	return job, nil
}

func (s *Service) DeliverCron(ctx context.Context, target channel.DeliveryTarget, text string) error {
	value, ok := s.senders.Load(target.Channel)
	if !ok {
		return fmt.Errorf("no proactive sender for channel %q", target.Channel)
	}
	sender, ok := value.(channel.ProactiveSender)
	if !ok {
		return fmt.Errorf("registered sender for channel %q is invalid", target.Channel)
	}
	return sender.SendText(ctx, target, text)
}

func (s *Service) RunCronAgent(ctx context.Context, job cronjob.Job) (string, error) {
	if s.codexClient == nil {
		return "", fmt.Errorf("codex client is not configured")
	}
	scopeID := cronAgentScopeID(job)
	msg := channel.Message{
		Channel:  job.Delivery.Channel,
		ChatID:   job.Delivery.ChatID,
		ThreadID: job.Delivery.ThreadID,
		ChatType: job.Delivery.ChatType,
		Target:   job.Delivery.Target,
		Text:     job.Payload.Text,
	}
	prompt := cronAgentPrompt(job.Payload.Text, time.Now())
	out := s.codexClient.RunWithOptions(ctx, scopeID, prompt, nil, codex.RunOptions{
		Sandbox:        s.resolveSandbox(msg),
		Channel:        job.Delivery.Channel,
		DisableCronMCP: true,
	})
	out = stripThinkingTags(out)
	if err := cronAgentOutputError(out); err != nil {
		return "", err
	}
	messages := parseCronAgentMessages(out)
	if len(messages) > 0 {
		for _, message := range messages {
			if err := s.DeliverCron(ctx, job.Delivery, message); err != nil {
				return "", err
			}
		}
		return "", cronjob.ErrAlreadyDelivered
	}
	return out, nil
}

type cronAgentEnvelope struct {
	Messages []cronAgentMessage `json:"messages"`
	Batches  []cronAgentMessage `json:"batches"`
	Parts    []cronAgentMessage `json:"parts"`
}

type cronAgentMessage struct {
	Title   string `json:"title"`
	Text    string `json:"text"`
	Content string `json:"content"`
}

func cronAgentPrompt(task string, now time.Time) string {
	return "[scheduled task]\nCurrent time: " + now.Format(time.RFC3339) + "\n\n" +
		"clawdex delivery instructions:\n" +
		"- Do not use external messaging tools from this scheduled run.\n" +
		"- If the user asks for multiple pushes, batches, or parts, return all messages in the JSON envelope below and clawdex will deliver them sequentially to the originating chat.\n" +
		"- Return only this JSON object when using multiple messages: {\"messages\":[{\"title\":\"...\",\"text\":\"...\"}]}.\n" +
		"- Each messages entry becomes one proactive chat message. Put the requested title in title and the body in text.\n" +
		"- If there is only one final response, normal text is acceptable.\n" +
		"- If an error occurs after you can still write a response, include the error explanation in the appropriate message entry.\n\n" +
		"user task:\n" + task
}

func parseCronAgentMessages(out string) []string {
	if messages := parseCronAgentJSONMessages(out); len(messages) > 0 {
		return messages
	}
	return parseCronAgentDelimitedMessages(out)
}

func parseCronAgentJSONMessages(out string) []string {
	raw := extractCronAgentJSON(out)
	if raw == "" {
		return nil
	}
	var env cronAgentEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil
	}
	items := env.Messages
	if len(items) == 0 {
		items = env.Batches
	}
	if len(items) == 0 {
		items = env.Parts
	}
	return formatCronAgentMessages(items)
}

func extractCronAgentJSON(out string) string {
	text := strings.TrimSpace(out)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
			text = strings.TrimSpace(text)
		}
	}
	if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
		return text
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return ""
}

func formatCronAgentMessages(items []cronAgentMessage) []string {
	var out []string
	for _, item := range items {
		text := strings.TrimSpace(firstNonEmpty(item.Text, item.Content))
		title := strings.TrimSpace(item.Title)
		switch {
		case title == "" && text == "":
			continue
		case title == "":
			out = append(out, text)
		case text == "":
			out = append(out, title)
		default:
			out = append(out, title+"\n\n"+text)
		}
	}
	return out
}

func parseCronAgentDelimitedMessages(out string) []string {
	if !strings.Contains(out, cronAgentMessageDelimiter) {
		return nil
	}
	parts := strings.Split(out, cronAgentMessageDelimiter)
	messages := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			messages = append(messages, part)
		}
	}
	return messages
}

func cronAgentScopeID(job cronjob.Job) int64 {
	key := strings.TrimSpace(job.ID)
	if key == "" {
		if job.ScopeID != 0 {
			return job.ScopeID
		}
		if job.Delivery.ChatID != 0 {
			return job.Delivery.ChatID
		}
		return scopeFallbackID
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("cron" + scopeSeparator + key))
	scopeID := int64(h.Sum64())
	if scopeID == 0 {
		return scopeFallbackID
	}
	return scopeID
}

func cronAgentOutputError(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(text, "codex failed:"),
		text == "codex command timeout",
		strings.HasPrefix(text, "failed to create temporary directory:"):
		return errors.New(text)
	default:
		return nil
	}
}

func cronRequestID(req cronToolRequest) string {
	if strings.TrimSpace(req.JobID) != "" {
		return strings.TrimSpace(req.JobID)
	}
	return strings.TrimSpace(req.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func filterCronJobsForDelivery(jobs []cronjob.Job, delivery channel.DeliveryTarget) []cronjob.Job {
	out := make([]cronjob.Job, 0, len(jobs))
	for _, job := range jobs {
		if sameCronDelivery(job.Delivery, delivery) {
			out = append(out, job)
		}
	}
	return out
}

func sameCronDelivery(a, b channel.DeliveryTarget) bool {
	if a.Channel != b.Channel || a.ThreadID != b.ThreadID || a.ChatType != b.ChatType {
		return false
	}
	aTarget := strings.TrimSpace(a.Target)
	bTarget := strings.TrimSpace(b.Target)
	if aTarget != "" && bTarget != "" {
		return aTarget == bTarget
	}
	return a.ChatID == b.ChatID
}

func writeCronToolJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v})
}

func writeCronToolError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

func (s *Service) logCronContext(msg channel.Message, token string) {
	logger.Debug("cron context created",
		"channel", msg.Channel,
		"chat", msg.ChatID,
		"thread", msg.ThreadID,
		"token_len", len(token),
	)
}
