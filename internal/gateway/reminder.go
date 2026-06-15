package gateway

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
)

var (
	mentionPattern          = regexp.MustCompile(`\s*@\S+(?:\([^)]*\))?`)
	relativeReminderPattern = regexp.MustCompile(`(?is)(\d+)\s*(秒|分钟|分|小时|钟头|天)\s*后\s*提醒(?:我)?(.+)$`)
	atReminderPattern       = regexp.MustCompile(`(?is)(今天|明天|后天)?\s*(凌晨|早上|上午|中午|下午|晚上)?\s*(\d{1,2})(?:\s*[:：]\s*(\d{1,2})|\s*点\s*(\d{1,2})?\s*分?)\s*提醒(?:我)?(.+)$`)
	dailySchedulePattern    = regexp.MustCompile(`(?is)(每天|每日)\s*(凌晨|早上|上午|中午|下午|晚上)?\s*(\d{1,2})(?:\s*[:：]\s*(\d{1,2})|\s*点\s*(\d{1,2})?\s*分?)\s*(.*)$`)
	timeHintPattern         = regexp.MustCompile(`(?is)(\d{1,2}\s*[:：]\s*\d{1,2}|\d{1,2}\s*点|\d+\s*(秒|分钟|分|小时|钟头|天)\s*后|每天|每日|今天|明天|后天|本周|下周|星期|周|本月|下个月|月|号)`)
)

var naturalCronNow = time.Now

type naturalCronRequest struct {
	Name     string
	Schedule cronjob.Schedule
	Payload  cronjob.Payload
	Summary  string
}

func (s *Service) handleNaturalCronRequest(ctx context.Context, msg channel.Message) (commandResponse, bool) {
	req, intent, err := parseNaturalCronRequest(naturalCronNow(), msg.Text)
	if !intent {
		return commandResponse{}, false
	}
	if s.cron == nil {
		return commandResponse{text: "Scheduled jobs are not enabled."}, true
	}
	if err != nil {
		return commandResponse{text: "I found a scheduling request, but could not create it: " + err.Error()}, true
	}
	job, err := s.cron.Add(ctx, cronjob.CreateJob{
		Name:     req.Name,
		Schedule: req.Schedule,
		Payload:  req.Payload,
		Delivery: deliveryTargetFromMessage(msg),
		ScopeID:  sessionScopeID(msg),
	})
	if err != nil {
		return commandResponse{text: "Failed to create scheduled job: " + err.Error()}, true
	}
	return commandResponse{text: formatNaturalCronCreated(job, req)}, true
}

func parseNaturalCronRequest(now time.Time, text string) (naturalCronRequest, bool, error) {
	text = cleanNaturalCronText(text)
	if text == "" {
		return naturalCronRequest{}, false, nil
	}
	if shouldDeferNaturalCronToAgent(text) {
		return naturalCronRequest{}, false, nil
	}
	if match := relativeReminderPattern.FindStringSubmatch(text); match != nil {
		return parseRelativeReminder(now, match)
	}
	if match := atReminderPattern.FindStringSubmatch(text); match != nil {
		return parseAtReminder(now, match)
	}
	if match := dailySchedulePattern.FindStringSubmatch(text); match != nil && dailyMatchHasAction(match[6]) {
		return parseDailySchedule(now, text, match)
	}
	if hasSchedulingCreateIntent(text) {
		return naturalCronRequest{}, true, fmt.Errorf("unsupported schedule format; try examples like \"5 分钟后提醒我喝水\", \"12:00提醒我午休\", or \"每天早上 10 点执行...\"")
	}
	return naturalCronRequest{}, false, nil
}

func parseRelativeReminder(now time.Time, match []string) (naturalCronRequest, bool, error) {
	amount, err := strconv.Atoi(match[1])
	if err != nil || amount <= 0 {
		return naturalCronRequest{}, true, fmt.Errorf("relative reminder interval must be positive")
	}
	duration, err := reminderDuration(amount, match[2])
	if err != nil {
		return naturalCronRequest{}, true, err
	}
	payload := cleanReminderPayload(match[3])
	if payload == "" {
		return naturalCronRequest{}, true, fmt.Errorf("reminder text is required")
	}
	runAt := now.Add(duration)
	return naturalCronRequest{
		Name:     "reminder",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleAt, At: runAt.Format(time.RFC3339)},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: payload},
		Summary:  payload,
	}, true, nil
}

func parseAtReminder(now time.Time, match []string) (naturalCronRequest, bool, error) {
	hour, minute, err := parseClock(match[2], match[3], firstNonEmpty(match[4], match[5]))
	if err != nil {
		return naturalCronRequest{}, true, err
	}
	payload := cleanReminderPayload(match[6])
	if payload == "" {
		return naturalCronRequest{}, true, fmt.Errorf("reminder text is required")
	}
	runAt := resolveOneShotTime(now, match[1], hour, minute)
	return naturalCronRequest{
		Name:     "reminder",
		Schedule: cronjob.Schedule{Kind: cronjob.ScheduleAt, At: runAt.Format(time.RFC3339)},
		Payload:  cronjob.Payload{Kind: cronjob.PayloadMessage, Text: payload},
		Summary:  payload,
	}, true, nil
}

func parseDailySchedule(now time.Time, original string, match []string) (naturalCronRequest, bool, error) {
	hour, minute, err := parseClock(match[2], match[3], firstNonEmpty(match[4], match[5]))
	if err != nil {
		return naturalCronRequest{}, true, err
	}
	body := strings.TrimSpace(match[6])
	payload := cronjob.Payload{Kind: cronjob.PayloadAgent, Text: original}
	name := "daily_task"
	if strings.Contains(body, "提醒") {
		if text := reminderTextAfterMarker(body); text != "" {
			payload = cronjob.Payload{Kind: cronjob.PayloadMessage, Text: text}
			name = "daily_reminder"
		}
	} else if text := taskTextAfterMarker(body); text != "" {
		payload.Text = text
	}
	if strings.TrimSpace(payload.Text) == "" {
		return naturalCronRequest{}, true, fmt.Errorf("scheduled task text is required")
	}
	return naturalCronRequest{
		Name: name,
		Schedule: cronjob.Schedule{
			Kind:     cronjob.ScheduleCron,
			Expr:     fmt.Sprintf("%d %d * * *", minute, hour),
			Timezone: cronTimezone(now.Location()),
		},
		Payload: payload,
		Summary: payload.Text,
	}, true, nil
}

func reminderDuration(amount int, unit string) (time.Duration, error) {
	switch unit {
	case "秒":
		return time.Duration(amount) * time.Second, nil
	case "分钟", "分":
		return time.Duration(amount) * time.Minute, nil
	case "小时", "钟头":
		return time.Duration(amount) * time.Hour, nil
	case "天":
		return time.Duration(amount) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported relative reminder unit %q", unit)
	}
}

func parseClock(period, hourText, minuteText string) (int, int, error) {
	hour, err := strconv.Atoi(strings.TrimSpace(hourText))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid hour")
	}
	minute := 0
	if strings.TrimSpace(minuteText) != "" {
		minute, err = strconv.Atoi(strings.TrimSpace(minuteText))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid minute")
		}
	}
	switch period {
	case "下午", "晚上":
		if hour < 12 {
			hour += 12
		}
	case "中午":
		if hour < 11 {
			hour += 12
		}
	}
	if hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("hour must be between 0 and 23")
	}
	if minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("minute must be between 0 and 59")
	}
	return hour, minute, nil
}

func resolveOneShotTime(now time.Time, dayText string, hour, minute int) time.Time {
	loc := now.Location()
	localNow := now.In(loc)
	runAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
	switch dayText {
	case "明天":
		runAt = runAt.AddDate(0, 0, 1)
	case "后天":
		runAt = runAt.AddDate(0, 0, 2)
	default:
		if !runAt.After(localNow) {
			runAt = runAt.AddDate(0, 0, 1)
		}
	}
	return runAt
}

func hasSchedulingCreateIntent(text string) bool {
	if !timeHintPattern.MatchString(text) {
		return false
	}
	if strings.Contains(text, "提醒") {
		return true
	}
	return strings.Contains(text, "定时任务") ||
		strings.Contains(text, "执行") ||
		strings.Contains(text, "运行")
}

func shouldDeferNaturalCronToAgent(text string) bool {
	if !hasSchedulingCreateIntent(text) {
		return false
	}
	if hasImmediateRunIntent(text) {
		return true
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"按这个",
		"用这个",
		"把这个",
		"这个创建",
		"这个定时",
		"这个 prompt",
		"这个prompt",
		"这段内容",
		"这条消息",
		"这份 prompt",
		"这份prompt",
		"上面",
		"上述",
		"以上",
		"前面",
		"前文",
		"前面的",
		"前一条",
		"上一条",
		"刚才",
		"之前整理",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func hasImmediateRunIntent(text string) bool {
	for _, marker := range []string{
		"现在触发一次",
		"立即触发一次",
		"马上触发一次",
		"立刻触发一次",
		"现在执行一次",
		"立即执行一次",
		"马上执行一次",
		"立刻执行一次",
		"现在运行一次",
		"立即运行一次",
		"马上运行一次",
		"立刻运行一次",
		"先触发一次",
		"先执行一次",
		"先运行一次",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func dailyMatchHasAction(body string) bool {
	return strings.Contains(body, "提醒") ||
		strings.Contains(body, "定时任务") ||
		strings.Contains(body, "执行") ||
		strings.Contains(body, "运行")
}

func cleanNaturalCronText(text string) string {
	text = stripFormatRunes(text)
	text = mentionPattern.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func cleanReminderPayload(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimLeft(text, " ：:，,。.!！")
	return strings.TrimSpace(text)
}

func reminderTextAfterMarker(text string) string {
	for _, marker := range []string{"提醒我", "提醒"} {
		if _, after, ok := strings.Cut(text, marker); ok {
			return cleanReminderPayload(after)
		}
	}
	return ""
}

func taskTextAfterMarker(text string) string {
	for _, marker := range []string{
		"执行以下定时任务",
		"执行定时任务",
		"执行以下任务",
		"运行以下定时任务",
		"运行定时任务",
		"运行以下任务",
		"执行",
		"运行",
		"以下定时任务",
		"定时任务",
		"以下任务",
	} {
		if _, after, ok := strings.Cut(text, marker); ok {
			return cleanReminderPayload(after)
		}
	}
	return cleanReminderPayload(text)
}

func cronTimezone(loc *time.Location) string {
	if loc == nil || loc.String() == "" || loc.String() == "Local" {
		return ""
	}
	return loc.String()
}

func formatNaturalCronCreated(job cronjob.Job, req naturalCronRequest) string {
	next := "-"
	if job.State.NextRunAt != nil {
		next = job.State.NextRunAt.In(time.Local).Format("2006-01-02 15:04:05 MST")
	}
	summary := req.Summary
	if len([]rune(summary)) > 120 {
		summary = string([]rune(summary)[:120]) + "..."
	}
	return fmt.Sprintf("Scheduled job created.\nID: `%s`\nNext: %s\nPayload: %s", shortCronID(job.ID), next, summary)
}
