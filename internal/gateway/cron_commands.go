package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Rememorio/clawdex/internal/channel"
	cronjob "github.com/Rememorio/clawdex/internal/cron"
)

var cronCommandDefs = []commandDef{
	{"/cron help", "Show scheduled job commands"},
	{"/cron list", "List scheduled jobs for this chat"},
	{"/cron status <id|index|name>", "Show a scheduled job"},
	{"/cron stop <id|index|name>", "Disable a scheduled job"},
	{"/cron resume <id|index|name>", "Re-enable a scheduled job"},
	{"/cron remove <id|index|name>", "Delete a scheduled job"},
	{"/cron clear", "Delete all scheduled jobs for this chat"},
}

func cronUsageText() string {
	var b strings.Builder
	b.WriteString("Usage:\n")
	appendCronCommands(&b)
	return strings.TrimRight(b.String(), "\n")
}

func appendCronHelpSection(b *strings.Builder) {
	b.WriteString("\nScheduled jobs:\n")
	appendCronCommands(b)
	b.WriteString("  Natural language requests are handled by the assistant through the cron tool.\n")
}

func appendCronCommands(b *strings.Builder) {
	for _, def := range cronCommandDefs {
		fmt.Fprintf(b, "  %s — %s\n", def.name, def.help)
	}
}

func (s *Service) handleCronCommand(ctx context.Context, msg channel.Message) (commandResponse, bool) {
	cmd, args, ok := parseCommandText(msg.Text)
	if !ok || cmd != "/cron" {
		return commandResponse{}, false
	}
	if s.cron == nil {
		return commandResponse{text: "Scheduled jobs are not enabled."}, true
	}
	fields := strings.Fields(args)
	action := "list"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	target := deliveryTargetFromMessage(msg)
	switch action {
	case "help":
		return commandResponse{text: cronUsageText()}, true
	case "list":
		jobs, err := s.currentCronJobs(ctx, target)
		if err != nil {
			return commandResponse{text: "Failed to list cron jobs: " + err.Error()}, true
		}
		return commandResponse{text: formatCronList(jobs)}, true
	case "clear":
		jobs, err := s.currentCronJobs(ctx, target)
		if err != nil {
			return commandResponse{text: "Failed to clear cron jobs: " + err.Error()}, true
		}
		removed := 0
		for _, job := range jobs {
			ok, err := s.cron.Remove(ctx, job.ID)
			if err != nil {
				return commandResponse{text: "Failed to remove cron job: " + err.Error()}, true
			}
			if ok {
				removed++
			}
		}
		return commandResponse{text: fmt.Sprintf("Removed %d scheduled job(s).", removed)}, true
	case "status", "stop", "resume", "remove":
		if len(fields) != 2 {
			return commandResponse{text: cronUsageText()}, true
		}
		jobs, err := s.currentCronJobs(ctx, target)
		if err != nil {
			return commandResponse{text: "Failed to read cron jobs: " + err.Error()}, true
		}
		job, err := selectCronJob(jobs, fields[1])
		if err != nil {
			return commandResponse{text: err.Error() + "\n\n" + cronUsageText()}, true
		}
		switch action {
		case "status":
			return commandResponse{text: formatCronDetails(job)}, true
		case "stop":
			enabled := false
			updated, err := s.cron.Update(ctx, job.ID, cronjob.PatchJob{Enabled: &enabled})
			if err != nil {
				return commandResponse{text: "Failed to stop cron job: " + err.Error()}, true
			}
			return commandResponse{text: "Stopped scheduled job: " + cronDisplayName(updated)}, true
		case "resume":
			enabled := true
			updated, err := s.cron.Update(ctx, job.ID, cronjob.PatchJob{Enabled: &enabled})
			if err != nil {
				return commandResponse{text: "Failed to resume cron job: " + err.Error()}, true
			}
			return commandResponse{text: "Resumed scheduled job: " + cronDisplayName(updated)}, true
		case "remove":
			removed, err := s.cron.Remove(ctx, job.ID)
			if err != nil {
				return commandResponse{text: "Failed to remove cron job: " + err.Error()}, true
			}
			if !removed {
				return commandResponse{text: "Scheduled job was already removed."}, true
			}
			return commandResponse{text: "Removed scheduled job: " + cronDisplayName(job)}, true
		}
	}
	return commandResponse{text: cronUsageText()}, true
}

func (s *Service) currentCronJobs(ctx context.Context, target channel.DeliveryTarget) ([]cronjob.Job, error) {
	jobs, err := s.cron.List(ctx, true)
	if err != nil {
		return nil, err
	}
	return filterCronJobsForDelivery(jobs, target), nil
}

func selectCronJob(jobs []cronjob.Job, selector string) (cronjob.Job, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return cronjob.Job{}, fmt.Errorf("cron selector is required")
	}
	if idx, err := strconv.Atoi(selector); err == nil {
		if idx >= 1 && idx <= len(jobs) {
			return jobs[idx-1], nil
		}
		return cronjob.Job{}, fmt.Errorf("cron selector out of range")
	}
	for _, job := range jobs {
		if job.ID == selector || shortCronID(job.ID) == selector {
			return job, nil
		}
	}
	for _, job := range jobs {
		if strings.EqualFold(job.Name, selector) && job.Name != "" {
			return job, nil
		}
	}
	return cronjob.Job{}, fmt.Errorf("cron job not found")
}

func formatCronList(jobs []cronjob.Job) string {
	if len(jobs) == 0 {
		return "No scheduled jobs for this chat."
	}
	var b strings.Builder
	b.WriteString("Scheduled jobs:\n")
	for i, job := range jobs {
		state := "enabled"
		if !job.Enabled {
			state = "disabled"
		}
		next := "-"
		if job.State.NextRunAt != nil {
			next = job.State.NextRunAt.Format("2006-01-02 15:04:05 MST")
		}
		fmt.Fprintf(&b, "%d. %s `%s` %s next=%s\n",
			i+1, cronDisplayName(job), shortCronID(job.ID), state, next)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCronDetails(job cronjob.Job) string {
	next := "-"
	if job.State.NextRunAt != nil {
		next = job.State.NextRunAt.Format("2006-01-02 15:04:05 MST")
	}
	last := "-"
	if job.State.LastRunAt != nil {
		last = job.State.LastRunAt.Format("2006-01-02 15:04:05 MST")
	}
	return fmt.Sprintf(
		"Scheduled job: %s\nID: `%s`\nEnabled: %t\nSchedule: %s\nPayload: %s\nNext: %s\nLast: %s\nLast status: %s\nRuns: %d",
		cronDisplayName(job),
		job.ID,
		job.Enabled,
		formatCronSchedule(job.Schedule),
		job.Payload.Kind,
		next,
		last,
		job.State.LastStatus,
		job.State.RunCount,
	)
}

func formatCronSchedule(schedule cronjob.Schedule) string {
	switch schedule.Kind {
	case cronjob.ScheduleAt:
		return "at " + schedule.At
	case cronjob.ScheduleEvery:
		return fmt.Sprintf("every %ds", schedule.EverySeconds)
	case cronjob.ScheduleCron:
		if schedule.Timezone != "" {
			return schedule.Expr + " " + schedule.Timezone
		}
		return schedule.Expr
	default:
		return schedule.Kind
	}
}

func cronDisplayName(job cronjob.Job) string {
	if strings.TrimSpace(job.Name) != "" {
		return job.Name
	}
	return shortCronID(job.ID)
}

func shortCronID(id string) string {
	if len(id) <= 13 {
		return id
	}
	return id[:13]
}
