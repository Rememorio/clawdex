// Package gateway wires channel drivers to the Codex execution backend.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
	"github.com/Rememorio/clawdex/internal/logger"
)

const (
	editThrottle       = 1 * time.Second
	draftThrottle      = 500 * time.Millisecond
	maxEditRunes       = 4096
	maxDraftRunes      = 4096
	streamingSuffixLen = 20
	streamingTextLimit = 3500
	defaultWorkerCount = 1
	defaultStreaming   = "partial"

	// Fallback finish texts used to close a thinking stream when
	// the agent produced no visible text output.
	finishTextMediaSent    = "📎 Files sent."
	finishTextDone         = "✅ Done."
	finishTextStillWorking = "⏳ Still working, will send the result when done..."

	// finishTextCancelled is shown both as the reply when /cancel hits a
	// non-streaming job and as the final stream frame text when /cancel
	// interrupts an in-flight streaming job. ❌ pairs visually with ✅ Done.
	finishTextCancelled = "❌ Cancelled."
)

// streamMaxAge is the maximum time a WeCom stream may remain open.
// WeCom caps stream updates at 6 minutes; we close early to guarantee delivery.
// Declared as var so tests can override it.
var streamMaxAge = 5 * time.Minute

// jobControl tracks the cancellation handle for an in-flight job and a flag
// that lets the running goroutine claim responsibility for surfacing a
// "Cancelled" message to the user. When selfAcksCancel is true, /cancel will
// skip its own Reply because the running goroutine is going to close the
// streaming message in place (e.g. WeCom FinishStream("❌ Cancelled.")). This
// avoids the user seeing two "❌ Cancelled." messages in a row.
type jobControl struct {
	cancel         context.CancelFunc
	selfAcksCancel atomic.Bool
}

// Service orchestrates channel drivers and dispatches work to Codex workers.
type Service struct {
	codexClient   *codex.Client
	workers       int
	streaming     string // "off", "partial" (default), "progress"
	chatLocks     sync.Map
	activeCancels sync.Map // key: chatScopeKey(msg) → *jobControl
	cron          *cronjob.Service
	senders       sync.Map // channel name → channel.ProactiveSender
	cronContexts  sync.Map // token → cronContext
}

// New creates a gateway service with the requested worker count.
func New(c *codex.Client, workers int, streaming string) *Service {
	if workers < 1 {
		workers = defaultWorkerCount
	}
	if streaming == "" {
		streaming = defaultStreaming
	}
	return &Service{codexClient: c, workers: workers, streaming: streaming}
}

func (s *Service) SetCron(cronSvc *cronjob.Service) {
	s.cron = cronSvc
}

func (s *Service) RegisterSender(sender channel.ProactiveSender) {
	if sender == nil || sender.Name() == "" {
		return
	}
	s.senders.Store(sender.Name(), sender)
}

type cronContext struct {
	Msg       channel.Message
	Delivery  channel.DeliveryTarget
	ScopeID   int64
	ExpiresAt time.Time
}

// Run starts all channel drivers and processes inbound messages until
// context cancellation or fatal driver error.
func (s *Service) Run(ctx context.Context, drivers ...channel.Driver) error {
	if s.cron != nil {
		if err := s.cron.Start(ctx); err != nil {
			return err
		}
		defer s.cron.Stop()
	}

	jobs := make(chan job)
	wgWorkers := sync.WaitGroup{}

	for i := 0; i < s.workers; i++ {
		wgWorkers.Add(1)
		go func() {
			defer wgWorkers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j, ok := <-jobs:
					if !ok {
						return
					}
					s.processJob(ctx, j)
				}
			}
		}()
	}

	wgDrivers := sync.WaitGroup{}
	errs := make(chan error, len(drivers))
	h := &handler{jobs: jobs}

	for _, d := range drivers {
		driver := d
		wgDrivers.Add(1)
		go func() {
			defer wgDrivers.Done()
			if err := driver.Start(ctx, h); err != nil && ctx.Err() == nil {
				errs <- err
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wgDrivers.Wait()
		close(jobs)
		wgWorkers.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		<-done
		return ctx.Err()
	case err := <-errs:
		<-done
		return err
	case <-done:
		return nil
	}
}

// cleanupMediaDirs removes temp directories created by channel media downloaders.
// CleanupPaths is the channel-owned cleanup contract; the gateway only removes
// clawdex media dirs under the system temp root.
func cleanupMediaDirs(pathSets ...[]string) {
	seen := make(map[string]bool)
	for _, paths := range pathSets {
		for _, p := range paths {
			dir := filepath.Dir(p)
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if isClawdexMediaTempDir(dir) {
				os.RemoveAll(dir)
			}
		}
	}
}

func isClawdexMediaTempDir(dir string) bool {
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, "clawdex-") || !strings.Contains(base, "-media-") {
		return false
	}

	parent := filepath.Clean(filepath.Dir(dir))
	tmpRoot := filepath.Clean(os.TempDir())
	if evalParent, err := filepath.EvalSymlinks(parent); err == nil {
		parent = evalParent
	}
	if evalRoot, err := filepath.EvalSymlinks(tmpRoot); err == nil {
		tmpRoot = evalRoot
	}
	return parent == tmpRoot
}

// resolveSandbox returns the sandbox level to use for a given message.
// Group messages use GroupSandbox if configured, otherwise the default Sandbox.
func (s *Service) resolveSandbox(msg channel.Message) string {
	if msg.ChatType == "group" && s.codexClient.GroupSandbox != "" {
		return s.codexClient.GroupSandbox
	}
	return s.codexClient.Sandbox
}

func (s *Service) lockChat(msg channel.Message) func() {
	key := msg.Channel + ":" + strconv.FormatInt(msg.ChatID, 10)
	lockVal, _ := s.chatLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lockVal.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// chatScopeKey returns the map key used for per-chat cancel tracking.
func chatScopeKey(msg channel.Message) string {
	return msg.Channel + ":" + strconv.FormatInt(msg.ChatID, 10)
}

// cancelRunningJob cancels the in-flight codex job for the given chat, if any.
// Returns the jobControl that was cancelled, or nil if no job was running.
// Callers can inspect ctrl.selfAcksCancel to decide whether to send their own
// confirmation Reply or whether the running goroutine will surface it.
func (s *Service) cancelRunningJob(msg channel.Message) *jobControl {
	key := chatScopeKey(msg)
	if val, ok := s.activeCancels.LoadAndDelete(key); ok {
		ctrl := val.(*jobControl)
		ctrl.cancel()
		return ctrl
	}
	return nil
}

func (s *Service) processJob(ctx context.Context, j job) {
	// Handle /cancel before acquiring chatLock to avoid deadlock with the
	// running job that holds the lock.
	if isCancel(j.msg.Text) {
		ctrl := s.cancelRunningJob(j.msg)
		cancelled := ctrl != nil
		logger.Info("cancel requested",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"cancelled", cancelled,
		)
		// If the running goroutine is in a stream path that can rewrite its
		// own message in place (e.g. WeCom FinishStream), let it surface the
		// cancellation — sending our own Reply too would show a duplicate.
		if cancelled && ctrl.selfAcksCancel.Load() {
			return
		}
		var reply string
		if cancelled {
			reply = finishTextCancelled
		} else {
			reply = "No running task to cancel."
		}
		if err := j.responder.Reply(ctx, j.msg, reply); err != nil {
			logger.Error("cancel reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
		}
		return
	}

	defer cleanupMediaDirs(j.msg.MediaPaths, j.msg.CleanupPaths)

	if resp, ok := s.handleCronCommand(ctx, j.msg); ok {
		logger.Info("cron command handled",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"cmd", j.msg.Text,
		)
		s.replyCommand(ctx, j, resp)
		return
	}

	unlock := s.lockChat(j.msg)
	defer unlock()

	// Create a cancellable context for this job.
	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()

	key := chatScopeKey(j.msg)
	ctrl := &jobControl{cancel: cancelJob}
	s.activeCancels.Store(key, ctrl)
	defer s.activeCancels.Delete(key)

	logger.Info("job received",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
		"msg", j.msg.MessageID,
		"text", j.msg.Text,
		"media", len(j.msg.MediaPaths),
	)

	// Handle slash commands.
	if resp, ok := handleCommand(s.codexClient, j.msg); ok {
		logger.Info("command handled",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"cmd", j.msg.Text,
		)
		s.replyCommand(ctx, j, resp)
		return
	}

	s.setReceivedReaction(ctx, j)

	// Send typing indicator.
	if err := j.responder.Typing(ctx, j.msg); err != nil {
		logger.Warn("typing failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
	}

	// Try streaming path.
	if s.streaming != "off" {
		if sr, ok := j.responder.(channel.StreamResponder); ok {
			// Tell /cancel to stay silent if the streaming goroutine can
			// rewrite its own message in place (FinishStream). This is
			// resolved before RunStream starts so a /cancel that races with
			// the very first chunk still finds the flag set correctly.
			if _, ok := sr.(channel.StreamFinisher); ok {
				ctrl.selfAcksCancel.Store(true)
			}
			logger.Info("codex stream started",
				"channel", j.msg.Channel,
				"chat", j.msg.ChatID,
			)
			s.runStreaming(jobCtx, j, sr)
			if jobCtx.Err() == context.Canceled {
				s.setCancelledReaction(ctx, j)
				return
			}
			s.setDoneReaction(ctx, j)
			return
		}
	}

	// Blocking codex run.
	logger.Info("codex run started",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
	)
	sandbox := s.resolveSandbox(j.msg)
	scopeID := sessionScopeID(j.msg)
	prompt := codexPrompt(j.msg)
	cronToken := s.newCronContext(j.msg)
	s.logCronContext(j.msg, cronToken)
	out := s.codexClient.RunWithOptions(
		jobCtx,
		scopeID,
		prompt,
		j.msg.MediaPaths,
		codex.RunOptions{
			Sandbox:          sandbox,
			Channel:          j.msg.Channel,
			CronContextToken: cronToken,
		},
	)
	out = stripThinkingTags(out)
	logger.Info("codex run finished",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
		"output", out,
	)
	if jobCtx.Err() == context.Canceled {
		s.setCancelledReaction(ctx, j)
		return
	}
	s.replyWithMediaDetection(ctx, j, out)
	s.setDoneReaction(ctx, j)
}

// setReceivedReaction sets a "received/working" reaction on the original message.
func (s *Service) setReceivedReaction(ctx context.Context, j job) {
	s.setStatusReaction(ctx, j, "👀")
}

// setDoneReaction sets a "done" reaction on the original message.
func (s *Service) setDoneReaction(ctx context.Context, j job) {
	s.setStatusReaction(ctx, j, "👍")
}

// setCancelledReaction sets a "cancelled" reaction on the original message.
func (s *Service) setCancelledReaction(ctx context.Context, j job) {
	s.setStatusReaction(ctx, j, "❌")
}

func (s *Service) setStatusReaction(ctx context.Context, j job, emoji string) {
	if sr, ok := j.responder.(channel.StatusReactor); ok {
		if err := sr.SetReaction(ctx, j.msg.ChatID, j.msg.MessageID, emoji); err != nil {
			logger.Warn("status reaction failed",
				"channel", j.msg.Channel,
				"chat", j.msg.ChatID,
				"msg", j.msg.MessageID,
				"emoji", emoji,
				"error", err,
			)
		}
	}
}

// replyCommand dispatches a command response, using keyboard if available.
func (s *Service) replyCommand(ctx context.Context, j job, resp commandResponse) {
	if resp.textOnly {
		if err := j.responder.Reply(ctx, j.msg, resp.text); err != nil {
			logger.Error("reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
		}
		return
	}

	// For /sessions, try the rich session card first.
	if resp.sessionCard != nil {
		if scr, ok := j.responder.(channel.SessionCardResponder); ok {
			if err := scr.ReplyWithSessionCard(ctx, j.msg, *resp.sessionCard); err != nil {
				logger.Error("session card reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
			} else {
				return
			}
		}
	}

	if resp.keyboard != nil {
		if kr, ok := j.responder.(channel.KeyboardResponder); ok {
			var rows [][]channel.KeyboardButton
			for _, row := range resp.keyboard {
				var r []channel.KeyboardButton
				for _, b := range row {
					r = append(r, channel.KeyboardButton{Text: b.text, CallbackData: b.callbackData})
				}
				rows = append(rows, r)
			}
			if err := kr.ReplyWithKeyboard(ctx, j.msg, resp.text, rows); err != nil {
				logger.Error("keyboard reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
			}
			return
		}
	}
	if err := j.responder.Reply(ctx, j.msg, resp.text); err != nil {
		logger.Error("reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
	}
}

// runStreaming uses codex streaming to edit a Telegram message in real-time.
func (s *Service) runStreaming(ctx context.Context, j job, sr channel.StreamResponder) {
	// Prefer draft streaming if supported.
	if dr, ok := sr.(channel.DraftResponder); ok {
		s.runDraftStreaming(ctx, j, dr)
		return
	}
	s.runEditStreaming(ctx, j, sr)
}

func suppressTextWithMedia(responder channel.Responder) bool {
	suppressor, ok := responder.(channel.MediaTextSuppressor)
	if !ok {
		return false
	}
	return suppressor.SuppressTextWithMedia()
}

// codexStreamContext mirrors RunStream's internal WithTimeout into the
// caller's ctx so the post-stream replay path can distinguish a forced
// timeout (DeadlineExceeded) from a genuine completion. RunStream still
// applies its own WithTimeout; both fire at the same instant because they
// derive from the same parent + duration.
func (s *Service) codexStreamContext(parent context.Context) (context.Context, context.CancelFunc) {
	if s.codexClient == nil || s.codexClient.Timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.codexClient.Timeout)
}

// resolveFinishText determines the text to send in the stream finish frame.
// Fallback priority:
//  1. Has visible text → use the full output text.
//  2. Has media files  → finishTextMediaSent.
//  3. Otherwise        → finishTextDone.
func (s *Service) resolveFinishText(out string, mediaPaths []string) string {
	if strings.TrimSpace(out) != "" {
		return out
	}
	if len(mediaPaths) > 0 {
		return finishTextMediaSent
	}
	return finishTextDone
}

// runEditStreaming uses send+edit pattern for streaming output.
// processJob pre-marks the in-flight job's selfAcksCancel before this is
// called, so we don't race a /cancel arriving before the first chunk.
func (s *Service) runEditStreaming(ctx context.Context, j job, sr channel.StreamResponder) { // Send a thinking placeholder if the driver supports it.
	thinkingSent := false
	if ti, ok := sr.(channel.ThinkingIndicator); ok {
		if err := ti.SendThinking(ctx, j.msg); err != nil {
			logger.Warn("thinking indicator failed",
				"chat", j.msg.ChatID, "error", err,
			)
		} else {
			thinkingSent = true
		}
	}

	sandbox := s.resolveSandbox(j.msg)
	suppressMediaText := suppressTextWithMedia(j.responder)
	var (
		mu       sync.Mutex
		msgID    int64
		lastText string
		pending  bool
		timer    *time.Timer
		fullText string
	)

	streamExpired := &atomic.Bool{}

	// Pre-emptively close the WeCom stream before the 6-minute platform limit.
	streamGuard := time.AfterFunc(streamMaxAge, func() {
		streamExpired.Store(true)

		// Stop the edit throttle timer so no more edits race with finish.
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()

		if sf, ok := sr.(channel.StreamFinisher); ok {
			mu.Lock()
			snapshot := stripThinkingTags(fullText)
			mu.Unlock()

			hint := finishTextStillWorking
			if snapshot != "" {
				hint = snapshot + "\n\n" + finishTextStillWorking
			}
			if err := sf.FinishStream(ctx, j.msg.ChatID, hint); err != nil {
				logger.Warn("stream guard finish failed", "chat", j.msg.ChatID, "error", err)
			} else {
				logger.Info("stream closed by guard timer (approaching 6-min wecom limit)",
					"chat", j.msg.ChatID)
			}
		}
	})
	defer streamGuard.Stop()

	flush := func() {
		if streamExpired.Load() {
			return
		}

		mu.Lock()
		text := fullText
		id := msgID
		pending = false
		mu.Unlock()

		if id == 0 || text == "" {
			return
		}

		// Truncate for edit if too long.
		if len([]rune(text)) > maxEditRunes {
			text = string([]rune(text)[:maxEditRunes-streamingSuffixLen]) + "\n... (streaming)"
		}

		if err := sr.EditMessage(ctx, j.msg.ChatID, id, text); err != nil {
			logger.Warn("edit message failed", "chat", j.msg.ChatID, "error", err)
		}
		mu.Lock()
		lastText = text
		mu.Unlock()
	}

	onChunk := func(text string) {
		if streamExpired.Load() {
			// Stream already closed; just accumulate text for final push.
			mu.Lock()
			fullText = stripThinkingTags(text)
			mu.Unlock()
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if thinkingSent {
			// Keep <think> tags for native WeCom rendering:
			// the client shows thinking content as gray text.
			fullText = text
		} else {
			fullText = stripThinkingTags(text)
		}

		if msgID == 0 {
			visibleText := stripThinkingTags(text)
			if suppressMediaText && len(extractFilePaths(visibleText)) > 0 {
				return
			}
			// First chunk: send the initial message.
			id, err := sr.SendMessage(ctx, j.msg, fullText)
			if err != nil {
				logger.Error("send message failed", "chat", j.msg.ChatID, "error", err)
				return
			}
			msgID = id
			lastText = fullText
			return
		}

		// Throttle edits.
		if !pending {
			pending = true
			if timer == nil {
				timer = time.AfterFunc(editThrottle, flush)
			} else {
				timer.Reset(editThrottle)
			}
		}
	}

	scopeID := sessionScopeID(j.msg)
	prompt := codexPrompt(j.msg)
	cronToken := s.newCronContext(j.msg)
	s.logCronContext(j.msg, cronToken)
	// Mirror RunStream's internal WithTimeout into our ctx so we can detect
	// "codex hit its CommandTimeout" via ctx.Err() == DeadlineExceeded after
	// RunStream returns. Without this, RunStream's child ctx swallows the
	// timeout signal and the post-stream replay path can't distinguish a
	// genuine completion from a forced timeout.
	streamCtx, cancelStream := s.codexStreamContext(ctx)
	defer cancelStream()
	out := s.codexClient.RunStreamWithOptions(
		streamCtx,
		scopeID,
		prompt,
		j.msg.MediaPaths,
		onChunk,
		codex.RunOptions{
			Sandbox:          sandbox,
			Channel:          j.msg.Channel,
			CronContextToken: cronToken,
		},
	)
	out = stripThinkingTags(out)

	logger.Info("codex stream finished",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
		"output", out,
	)

	// Snapshot the streaming state under the lock. fullText is the cumulative
	// view the user already saw via onChunk (multi-message Codex turns are
	// joined with "---" separators inside RunStream's accumulator); out is
	// only the *last* agent_message. When the stream was closed early by the
	// guard timer we replay fullText, not out — replaying just the last
	// agent_message is the bug that surfaced as a short, mid-thought reply
	// arriving long after the user's "Still working..." indicator.
	mu.Lock()
	if timer != nil {
		timer.Stop()
	}
	finalMsgID := msgID
	streamSnapshot := stripThinkingTags(fullText)
	mu.Unlock()

	if streamExpired.Load() {
		streamErr := streamCtx.Err()
		// /cancel + streamExpired is a rare corner: the wecom stream id has
		// already been consumed by the guard's FinishStream, and processJob's
		// /cancel branch saw selfAcksCancel=true so it skipped its own Reply.
		// Best we can do here is not pile on a duplicate; the user will have
		// to retry /cancel if no notice appears (TODO: reset selfAcksCancel
		// from the guard so /cancel falls back to Reply).
		if streamErr == context.Canceled {
			logger.Info("stream cancelled after guard expired, suppressing replay",
				"channel", j.msg.Channel,
				"chat", j.msg.ChatID,
			)
			return
		}
		// Stream was closed early; deliver the final output as a new message.
		// If Codex completed normally, prefer the final agent_message (`out`),
		// matching the non-expired final edit path and avoiding replay of
		// intermediary status messages that were only useful during streaming.
		// If Codex itself timed out, the cumulative snapshot is more useful
		// because `out` may be empty or only the last partial fragment.
		replay := out
		if streamErr == context.DeadlineExceeded {
			replay = streamSnapshot
			if replay == "" {
				replay = out
			}
		} else if replay == "" {
			replay = streamSnapshot
		}
		if streamErr == context.DeadlineExceeded && s.codexClient != nil && s.codexClient.Timeout > 0 {
			prefix := fmt.Sprintf("⏰ Codex hit timeout (%s). Here is what it produced before being stopped:\n\n",
				s.codexClient.Timeout)
			if replay == "" {
				replay = prefix + "(no output)"
			} else {
				replay = prefix + replay
			}
		}
		logger.Info("codex completed after stream expired, sending result as new message",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"timed_out", streamErr == context.DeadlineExceeded,
			"replay_bytes", len(replay),
		)
		s.replyWithMediaDetection(ctx, j, replay)
		return
	}

	// /cancel was issued while codex was streaming. Codex's SIGINT grace
	// period (5s) lets it emit a partial agent_message before exit; without
	// this branch the stream goroutine would push that partial as the final
	// reply on top of /cancel's confirmation message — surfacing as two
	// near-identical messages to the user.
	if ctx.Err() == context.Canceled {
		logger.Info("stream cancelled, suppressing final reply",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"had_msg", finalMsgID != 0,
			"partial_bytes", len(out),
		)
		if sf, ok := sr.(channel.StreamFinisher); ok {
			// Use a detached context so we can still close the stream after
			// jobCtx has been cancelled. processJob's /cancel branch saw
			// selfAcksCancel=true and skipped its own Reply, so this
			// FinishStream is the only "❌ Cancelled." the user will see.
			finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancelFinish()
			if err := sf.FinishStream(finishCtx, j.msg.ChatID, finishTextCancelled); err != nil {
				logger.Warn("finish stream after cancel failed", "chat", j.msg.ChatID, "error", err)
			}
			return
		}
		// Edit-only driver (e.g. Telegram): leave the partial message in
		// place and let processJob's /cancel branch surface "❌ Cancelled."
		// as a separate reply. Pushing the full final via EditMessage here
		// is exactly the duplicate-message bug we are fixing.
		return
	}

	paths := extractFilePaths(out)

	// Final edit with complete text.
	if finalMsgID != 0 {
		if len(paths) > 0 && suppressMediaText {
			logger.Info("stream media detected, suppressing text",
				"chat", j.msg.ChatID,
				"files", len(paths),
			)
			if lastText != "" {
				if err := sr.EditMessage(ctx, j.msg.ChatID, finalMsgID, ""); err != nil {
					logger.Warn("final edit failed", "chat", j.msg.ChatID, "error", err)
				}
			}
			if sf, ok := sr.(channel.StreamFinisher); ok {
				finishText := finishTextMediaSent
				if err := sf.FinishStream(ctx, j.msg.ChatID, finishText); err != nil {
					logger.Warn("finish stream failed", "chat", j.msg.ChatID, "error", err)
				}
			}
			s.sendMediaIfPresent(ctx, j, out)
			return
		}

		if sf, ok := sr.(channel.StreamFinisher); ok {
			finishText := s.resolveFinishText(out, paths)
			if err := sf.FinishStream(ctx, j.msg.ChatID, finishText); err != nil {
				logger.Warn("finish stream failed", "chat", j.msg.ChatID, "error", err)
				s.replyWithMediaDetection(ctx, j, out)
				return
			}
			s.sendMediaIfPresent(ctx, j, out)
			return
		}

		chunks := splitByRuneLimit(out, streamingTextLimit)
		if len(chunks) == 1 {
			if out != lastText {
				if err := sr.EditMessage(ctx, j.msg.ChatID, finalMsgID, out); err != nil {
					logger.Warn("final edit failed", "chat", j.msg.ChatID, "error", err)
				}
			}
		} else {
			// Multi-chunk: edit first message, send the rest as new messages.
			if err := sr.EditMessage(ctx, j.msg.ChatID, finalMsgID, chunks[0]); err != nil {
				logger.Warn("final edit failed", "chat", j.msg.ChatID, "error", err)
			}
			for _, chunk := range chunks[1:] {
				if err := sr.Reply(ctx, j.msg, chunk); err != nil {
					logger.Warn("reply chunk failed", "chat", j.msg.ChatID, "error", err)
				}
			}
		}
		// Send media if detected.
		s.sendMediaIfPresent(ctx, j, out)
	} else {
		// Streaming produced no chunks — fall back to normal reply.
		// If thinking was sent, we still need to close the stream.
		if thinkingSent {
			if sf, ok := sr.(channel.StreamFinisher); ok {
				finishText := s.resolveFinishText(out, extractFilePaths(out))
				if err := sf.FinishStream(ctx, j.msg.ChatID, finishText); err != nil {
					logger.Warn("finish stream failed", "chat", j.msg.ChatID, "error", err)
				} else {
					// Stream finished successfully with the final text —
					// no need to send a separate reply message.
					logger.Info("stream fallback to finish",
						"channel", j.msg.Channel,
						"chat", j.msg.ChatID,
					)
					s.sendMediaIfPresent(ctx, j, out)
					return
				}
			}
		}
		logger.Info("stream fallback to reply",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"files", len(paths),
		)
		s.replyWithMediaDetection(ctx, j, out)
	}
}

// replyWithMediaDetection sends the codex output, detecting and sending any
// file paths as media attachments.
func (s *Service) replyWithMediaDetection(ctx context.Context, j job, out string) {
	if mr, ok := j.responder.(channel.MediaResponder); ok {
		if paths := extractFilePaths(out); len(paths) > 0 {
			logger.Info("media detected in reply",
				"channel", j.msg.Channel,
				"chat", j.msg.ChatID,
				"files", len(paths),
			)
			caption := out
			if suppressTextWithMedia(j.responder) {
				caption = ""
			}
			if err := mr.ReplyWithMedia(ctx, j.msg, caption, paths); err != nil {
				logger.Warn("media reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
				// Fall back to text reply.
				if err := j.responder.Reply(ctx, j.msg, out); err != nil {
					logger.Error("reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
				}
			}
			return
		}
	}
	logger.Info("text reply sent",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
		"len", len(out),
	)
	if err := j.responder.Reply(ctx, j.msg, out); err != nil {
		logger.Error("reply failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
	}
}

// sendMediaIfPresent detects file paths in text and sends them as media.
func (s *Service) sendMediaIfPresent(ctx context.Context, j job, text string) {
	if mr, ok := j.responder.(channel.MediaResponder); ok {
		if paths := extractFilePaths(text); len(paths) > 0 {
			logger.Info("sending media files",
				"channel", j.msg.Channel,
				"chat", j.msg.ChatID,
				"files", len(paths),
			)
			if err := mr.ReplyWithMedia(ctx, j.msg, "", paths); err != nil {
				logger.Warn("media send failed", "channel", j.msg.Channel, "chat", j.msg.ChatID, "error", err)
			}
		}
	}
}

// runDraftStreaming sends an initial message on the first chunk, then edits it
// as new chunks arrive (multi-turn tool-use scenarios). For single-turn
// responses Codex emits only one chunk, so the message appears in full — no
// simulated typing effect.
func (s *Service) runDraftStreaming(ctx context.Context, j job, dr channel.DraftResponder) {
	sandbox := s.resolveSandbox(j.msg)
	suppressMediaText := suppressTextWithMedia(j.responder)
	var (
		mu       sync.Mutex
		msgID    int64
		lastText string
		fullText string
		pending  bool
		timer    *time.Timer
	)

	flush := func() {
		mu.Lock()
		text := fullText
		id := msgID
		pending = false
		mu.Unlock()

		if id == 0 || text == "" {
			return
		}

		if len([]rune(text)) > maxDraftRunes {
			text = string([]rune(text)[:maxDraftRunes-streamingSuffixLen]) + "\n... (streaming)"
		}

		if err := dr.FinalizeDraft(ctx, j.msg.ChatID, id, text); err != nil {
			logger.Warn("draft edit failed", "chat", j.msg.ChatID, "error", err)
		}
		mu.Lock()
		lastText = text
		mu.Unlock()
	}

	onChunk := func(text string) {
		mu.Lock()
		defer mu.Unlock()

		fullText = stripThinkingTags(text)

		if msgID == 0 {
			if suppressMediaText && len(extractFilePaths(fullText)) > 0 {
				return
			}
			// First chunk: send the initial message.
			id, err := dr.SendDraft(ctx, j.msg, fullText)
			if err != nil {
				logger.Error("draft send failed", "chat", j.msg.ChatID, "error", err)
				return
			}
			msgID = id
			lastText = fullText
			return
		}

		if !pending {
			pending = true
			if timer == nil {
				timer = time.AfterFunc(draftThrottle, flush)
			} else {
				timer.Reset(draftThrottle)
			}
		}
	}

	scopeID := sessionScopeID(j.msg)
	prompt := codexPrompt(j.msg)
	cronToken := s.newCronContext(j.msg)
	s.logCronContext(j.msg, cronToken)
	streamCtx, cancelStream := s.codexStreamContext(ctx)
	defer cancelStream()
	out := s.codexClient.RunStreamWithOptions(
		streamCtx,
		scopeID,
		prompt,
		j.msg.MediaPaths,
		onChunk,
		codex.RunOptions{
			Sandbox:          sandbox,
			Channel:          j.msg.Channel,
			CronContextToken: cronToken,
		},
	)
	out = stripThinkingTags(out)

	logger.Info("codex draft stream finished",
		"channel", j.msg.Channel,
		"chat", j.msg.ChatID,
		"output", out,
	)

	mu.Lock()
	if timer != nil {
		timer.Stop()
	}
	finalMsgID := msgID
	mu.Unlock()

	// /cancel was issued mid-stream. Skip pushing the partial as a final
	// reply; processJob's /cancel branch will surface "❌ Cancelled." (or, if
	// the underlying driver supports FinishStream, this goroutine takes that
	// over via selfAcksCancel).
	if ctx.Err() == context.Canceled {
		logger.Info("draft stream cancelled, suppressing final reply",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
			"had_msg", finalMsgID != 0,
			"partial_bytes", len(out),
		)
		if sf, ok := any(dr).(channel.StreamFinisher); ok {
			finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancelFinish()
			if err := sf.FinishStream(finishCtx, j.msg.ChatID, finishTextCancelled); err != nil {
				logger.Warn("finish draft stream after cancel failed", "chat", j.msg.ChatID, "error", err)
			}
		}
		return
	}

	// Final edit with complete text.
	if finalMsgID != 0 {
		s.finalizeDraft(ctx, j, dr, finalMsgID, out, lastText)
	} else {
		// No chunks arrived — fall back to normal reply.
		logger.Info("draft stream fallback to reply",
			"channel", j.msg.Channel,
			"chat", j.msg.ChatID,
		)
		s.replyWithMediaDetection(ctx, j, out)
	}
}

// finalizeDraft performs the final edit(s) and media detection for completed streaming.
func (s *Service) finalizeDraft(ctx context.Context, j job, dr channel.DraftResponder, msgID int64, out, lastText string) {
	paths := extractFilePaths(out)
	if len(paths) > 0 && suppressTextWithMedia(j.responder) {
		if lastText != "" {
			if err := dr.FinalizeDraft(ctx, j.msg.ChatID, msgID, ""); err != nil {
				logger.Warn("final draft edit failed", "chat", j.msg.ChatID, "error", err)
			}
		}
		s.sendMediaIfPresent(ctx, j, out)
		return
	}

	chunks := splitByRuneLimit(out, streamingTextLimit)
	if len(chunks) == 1 {
		if out != lastText {
			if err := dr.FinalizeDraft(ctx, j.msg.ChatID, msgID, out); err != nil {
				logger.Warn("final draft edit failed", "chat", j.msg.ChatID, "error", err)
			}
		}
	} else {
		if err := dr.FinalizeDraft(ctx, j.msg.ChatID, msgID, chunks[0]); err != nil {
			logger.Warn("final draft edit failed", "chat", j.msg.ChatID, "error", err)
		}
		for _, chunk := range chunks[1:] {
			if err := dr.Reply(ctx, j.msg, chunk); err != nil {
				logger.Warn("reply chunk failed", "chat", j.msg.ChatID, "error", err)
			}
		}
	}
	s.sendMediaIfPresent(ctx, j, out)
}

// filePathRe matches absolute paths or home-relative path candidates.
var filePathRe = regexp.MustCompile(
	"((?:/|~/)[^\\s<>()\\[\\]{}\"']+)",
)

// codeBlockRe matches fenced code blocks (``` ... ```).
var codeBlockRe = regexp.MustCompile("(?s)```[^`]*```")

// inlineCodeRe matches inline code spans (` ... `), non-greedy.
var inlineCodeRe = regexp.MustCompile("`[^`]+`")

// stripCodeSpans removes fenced code blocks and inline code from text so that
// paths mentioned purely as code references are not treated as media to send.
func stripCodeSpans(text string) string {
	text = codeBlockRe.ReplaceAllString(text, "")
	text = inlineCodeRe.ReplaceAllString(text, "")
	return text
}

const pathTrimRightChars = ".,:;!?`*。，：；！？"

// extractFilePaths finds file paths in text that actually exist on disk.
// Paths inside markdown code spans (backticks) are ignored.
func extractFilePaths(text string) []string {
	text = stripCodeSpans(text)
	matches := filePathRe.FindAllString(text, -1)
	seen := make(map[string]bool)
	seenFiles := make(map[string]bool)
	var paths []string
	for _, m := range matches {
		path := normalizeDetectedPath(m)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		key, err := fileDedupKey(path)
		if err == nil && seenFiles[key] {
			continue
		}
		if err == nil {
			seenFiles[key] = true
		}
		paths = append(paths, path)
	}
	return paths
}

func fileDedupKey(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return filepath.Clean(path), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return strconv.FormatInt(info.Size(), 10) + ":" +
		hex.EncodeToString(sum[:]), nil
}

func normalizeDetectedPath(path string) string {
	cleaned := strings.TrimSpace(path)
	cleaned = strings.TrimRight(cleaned, pathTrimRightChars)
	cleaned = expandHomePath(cleaned)
	if strings.TrimSpace(cleaned) == "" {
		return ""
	}
	return filepath.Clean(cleaned)
}

func expandHomePath(path string) string {
	cleaned := strings.TrimSpace(path)
	if !strings.HasPrefix(cleaned, "~/") {
		return cleaned
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return cleaned
	}
	return filepath.Join(home, cleaned[2:])
}

// splitByRuneLimit splits text into chunks of at most `limit` runes.
func splitByRuneLimit(text string, limit int) []string {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return []string{"(empty response)"}
	}
	runes := []rune(clean)
	if len(runes) <= limit {
		return []string{clean}
	}
	parts := make([]string, 0, (len(runes)/limit)+1)
	for start := 0; start < len(runes); start += limit {
		end := min(start+limit, len(runes))
		parts = append(parts, string(runes[start:end]))
	}
	return parts
}

type job struct {
	msg       channel.Message
	responder channel.Responder
}

// HandleCardEvent implements channel.CardEventHandler. It processes a card
// button click synchronously by executing the mapped slash command and
// returning the resulting SessionCard. This avoids the async job queue so
// the WeCom driver can respond within the platform's timeout window.
func (s *Service) HandleCardEvent(_ context.Context, msg channel.Message, eventKey string, selectedID string) *channel.SessionCard {
	// Map event keys to synthetic slash command text.
	var syntheticText string
	switch eventKey {
	case "/sessions:switch":
		if selectedID != "" {
			syntheticText = "/resume " + selectedID
		} else {
			syntheticText = "/sessions"
		}
	case "/sessions:new":
		syntheticText = "/new"
	case "/sessions:status":
		syntheticText = "/status"
	case "/sessions:sessions":
		syntheticText = "/sessions"
	case "/sessions:help":
		syntheticText = "/help"
	default:
		return nil
	}

	msg.Text = syntheticText
	resp, ok := handleCommand(s.codexClient, msg)
	if !ok {
		return nil
	}
	return resp.sessionCard
}

// BuildWelcomeCard returns the card shown when a user enters a fresh chat.
// Buttons reuse the existing /sessions:* event keys so HandleCardEvent
// handles taps without any extra wiring.
func (s *Service) BuildWelcomeCard(_ context.Context, _ channel.Message) *channel.SessionCard {
	return &channel.SessionCard{
		Title: "Welcome to clawdex",
		Desc:  "I'm your codex copilot. Pick a command to get started.",
		Body: "• /help — show all commands\n" +
			"• /sessions — browse recent sessions\n" +
			"• /status — current chat context\n" +
			"• /new — start a fresh session",
		Buttons: []channel.SessionCardButton{
			{Text: "/help", CallbackData: "/sessions:help"},
			{Text: "/sessions", CallbackData: "/sessions:sessions"},
		},
	}
}

type handler struct {
	jobs chan<- job
}

func (h *handler) Handle(_ context.Context, msg channel.Message, responder channel.Responder) {
	logger.Info("job dispatched",
		"channel", msg.Channel,
		"chat", msg.ChatID,
		"msg", msg.MessageID,
	)
	h.jobs <- job{msg: msg, responder: responder}
}
