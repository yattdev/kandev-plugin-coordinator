package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (p *Plugin) RunDue(ctx context.Context, now time.Time) error {
	config, err := p.config(ctx)
	if err != nil {
		return err
	}
	if !config.MonitoringEnabled {
		return nil
	}
	if err := config.ReadyForRun(); err != nil {
		return err
	}
	workspaces, err := p.listAllWorkspaces(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, workspace := range workspaces {
		if workspace.ID == "" {
			continue
		}
		if err := p.runWorkspaceDue(ctx, workspace.ID, config, now); err != nil {
			failures = append(failures, fmt.Errorf("workspace %s: %w", workspace.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (p *Plugin) runWorkspaceDue(ctx context.Context, workspaceID string, config Config, now time.Time) error {
	state, err := p.readState(ctx, workspaceID)
	if err != nil {
		return err
	}
	standupKey, standupDate, standupDue, err := dailyOccurrence(workspaceID, now, config)
	if err != nil {
		return err
	}
	if standupDue && state.Schedule.LastStandupDate != standupDate {
		return p.dispatchAndRecord(ctx, workspaceID, config, TriggerStandup, standupKey, now)
	}
	if !state.Schedule.Armed {
		return nil
	}
	cycleKey, eligible, err := CycleOccurrenceKey(workspaceID, now, config)
	if err != nil || !eligible {
		return err
	}
	if state.Schedule.LastCycleSlot == cycleKey {
		return nil
	}
	return p.dispatchAndRecord(ctx, workspaceID, config, TriggerCycle, cycleKey, now)
}

func (p *Plugin) RunManual(ctx context.Context, workspaceID, trigger, idempotencyKey string) (DispatchResult, error) {
	if trigger != TriggerCycle && trigger != TriggerStandup {
		return DispatchResult{}, fmt.Errorf("manual trigger must be cycle or standup")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return DispatchResult{}, fmt.Errorf("idempotency_key is required")
	}
	config, err := p.config(ctx)
	if err != nil {
		return DispatchResult{}, err
	}
	if err := config.ReadyForRun(); err != nil {
		return DispatchResult{}, err
	}
	key := fmt.Sprintf("manual/%s/%s/%s", workspaceID, trigger, idempotencyKey)
	return p.dispatchOccurrence(ctx, workspaceID, config, trigger, key)
}

func (p *Plugin) dispatchAndRecord(ctx context.Context, workspaceID string, config Config, trigger, occurrenceKey string, now time.Time) error {
	result, dispatchErr := p.dispatchOccurrence(ctx, workspaceID, config, trigger, occurrenceKey)
	statusValue := result.Status
	if dispatchErr != nil {
		statusValue = "failed"
	}
	_, stateErr := p.updateDocument(ctx, workspaceID, func(doc *workspaceDocument) error {
		doc.State.Schedule.LastDispatch = DispatchStatus{Trigger: trigger, OccurrenceKey: occurrenceKey, Status: statusValue, At: now.UTC().Format(time.RFC3339Nano)}
		if dispatchErr != nil {
			doc.State.Schedule.LastDispatch.Error = dispatchErr.Error()
		}
		if trigger == TriggerStandup {
			_, date, _, _ := dailyOccurrence(workspaceID, now, config)
			doc.State.Schedule.LastStandupDate = date
			if result.Successful() {
				doc.State.Schedule.Armed = true
			}
		} else {
			doc.State.Schedule.LastCycleSlot = occurrenceKey
		}
		if result.Successful() {
			doc.State.Schedule.LastSuccessfulAt = now.UTC().Format(time.RFC3339Nano)
		} else {
			body := fmt.Sprintf("Coordinator %s dispatch returned %s.", trigger, statusValue)
			if dispatchErr != nil {
				body = fmt.Sprintf("Coordinator %s dispatch failed: %v", trigger, dispatchErr)
			}
			doc.Reports = append([]ReportArtifact{{
				ID: occurrenceKey + "-status", Type: ReportStatus,
				Title: "Coordinator dispatch status", Body: body,
				CreatedAt: now.UTC().Format(time.RFC3339Nano), Trigger: trigger,
				OccurrenceKey: occurrenceKey,
			}}, doc.Reports...)
		}
		return nil
	})
	if dispatchErr != nil {
		return errors.Join(dispatchErr, stateErr)
	}
	return stateErr
}

func (p *Plugin) dispatchOccurrence(ctx context.Context, workspaceID string, config Config, trigger, occurrenceKey string) (DispatchResult, error) {
	checks, err := p.selectedChecks(ctx, workspaceID)
	if err != nil {
		return DispatchResult{}, err
	}
	if len(checks) == 0 {
		return DispatchResult{}, fmt.Errorf("no workflow steps are configured for monitoring")
	}
	if _, err := ensureConversation(ctx, p.manager, workspaceID, config); err != nil {
		return DispatchResult{}, err
	}
	prompt := ComposeOccurrencePrompt(config.BasePrompt, checks, trigger, config.ReportTemplate)
	result, err := p.manager.Dispatch(ctx, DispatchRequest{WorkspaceID: workspaceID, Key: ConversationKey, OccurrenceKey: occurrenceKey, Prompt: prompt})
	if err != nil {
		return DispatchResult{}, err
	}
	if result.OccurrenceKey == "" {
		result.OccurrenceKey = occurrenceKey
	}
	return result, nil
}

func dailyOccurrence(workspaceID string, now time.Time, config Config) (key, localDate string, due bool, err error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return "", "", false, err
	}
	clock, err := parseClock(config.DailyReportTime)
	if err != nil {
		return "", "", false, err
	}
	local := now.In(location)
	date := local.Format("2006-01-02")
	if !allowedDay(local, config.ScheduleDays) {
		return "", date, false, nil
	}
	dueAt := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	return fmt.Sprintf("scheduled/%s/standup/%s", workspaceID, date), date, !local.Before(dueAt), nil
}

func CycleOccurrenceKey(workspaceID string, now time.Time, config Config) (string, bool, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return "", false, err
	}
	startClock, err := parseClock(config.WindowStart)
	if err != nil {
		return "", false, err
	}
	endClock, err := parseClock(config.WindowEnd)
	if err != nil {
		return "", false, err
	}
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), startClock.Hour(), startClock.Minute(), 0, 0, location)
	end := time.Date(local.Year(), local.Month(), local.Day(), endClock.Hour(), endClock.Minute(), 0, 0, location)
	if !end.After(start) {
		if local.Before(end) {
			start = start.AddDate(0, 0, -1)
		} else {
			end = end.AddDate(0, 0, 1)
		}
	}
	if local.Before(start) || !local.Before(end) || !allowedDay(start, config.ScheduleDays) {
		return "", false, nil
	}
	slot := int(local.Sub(start) / (time.Duration(config.CycleIntervalMinutes) * time.Minute))
	return fmt.Sprintf("scheduled/%s/cycle/%s/%d", workspaceID, start.Format("2006-01-02"), slot), true, nil
}

func allowedDay(value time.Time, mode string) bool {
	if mode == ScheduleEveryDay {
		return true
	}
	return value.Weekday() != time.Saturday && value.Weekday() != time.Sunday
}
