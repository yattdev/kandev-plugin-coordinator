package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const coordinatorTaskTitle = "Coordinator scheduled check"

// dispatchDue evaluates all configured workspace policies. It is intentionally
// callable independently from the ticker so scheduling decisions are testable.
func (p *coordinatorPlugin) dispatchDue(ctx context.Context, now time.Time) error {
	if p.Host() == nil {
		return fmt.Errorf("coordinator: host unavailable")
	}
	values, err := p.Host().GetConfig(ctx)
	if err != nil {
		return err
	}
	config, err := configFrom(values)
	if err != nil {
		return err
	}
	if config.AgentProfile == "" {
		return fmt.Errorf("coordinator: agent_profile is required for scheduled runs")
	}
	workspaces, _, err := p.Host().Workspaces().List(ctx, pluginsdk.Page{Limit: 100})
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if err := p.dispatchWorkspaceDue(ctx, now, workspace.ID, config); err != nil {
			return err
		}
	}
	return nil
}

func (p *coordinatorPlugin) dispatchWorkspaceDue(ctx context.Context, now time.Time, workspaceID string, config Config) error {
	state, err := p.state(ctx, workspaceID)
	if err != nil {
		return err
	}
	cycleDue := dueSince(state.LastCycleAt, now, time.Duration(config.CycleIntervalMinutes)*time.Minute)
	reportDue, err := standupDue(state.LastReportDispatchAt, now, config)
	if err != nil {
		return err
	}
	if !cycleDue && !reportDue {
		return nil
	}
	workflows, _, err := p.Host().Workflows().List(ctx, workspaceID, pluginsdk.Page{Limit: 100})
	if err != nil {
		return err
	}
	dispatchedCycle, dispatchedReport := false, false
	for _, workflow := range workflows {
		policy, found, err := p.workflowPolicy(ctx, workspaceID, workflow.ID)
		if err != nil {
			return err
		}
		if !found || len(policy.Worksteps) == 0 {
			continue
		}
		for _, workstep := range policy.Worksteps {
			if cycleDue {
				if err := p.dispatchRun(ctx, workspaceID, workflow, workstep, config, false); err != nil {
					return err
				}
				dispatchedCycle = true
			}
			if reportDue && !dispatchedReport {
				if err := p.dispatchRun(ctx, workspaceID, workflow, workstep, config, true); err != nil {
					return err
				}
				dispatchedReport = true
			}
		}
	}
	if dispatchedCycle {
		state.LastCycleAt = now.UTC().Format(time.RFC3339)
	}
	if dispatchedReport {
		state.LastReportDispatchAt = now.UTC().Format(time.RFC3339)
	}
	if dispatchedCycle || dispatchedReport {
		return p.saveState(ctx, workspaceID, state)
	}
	return nil
}

func (p *coordinatorPlugin) dispatchRun(ctx context.Context, workspaceID string, workflow pluginsdk.Workflow, workstep WorkstepPolicy, config Config, report bool) error {
	prompt := composePrompt(config.BasePrompt, workstep.Prompt)
	title := coordinatorTaskTitle
	if report {
		title = "Coordinator daily report"
		prompt += "\n\nProduce the weekday coordinator report and persist it with the coordinator_record_report tool."
	}
	profileID := config.AgentProfile
	stepID := workstep.WorkstepID
	task, err := p.Host().Tasks().Create(ctx, pluginsdk.CreateTaskInput{
		WorkspaceID: workspaceID, WorkflowID: workflow.ID, WorkflowStepID: &stepID,
		Title: title, Description: "Scheduled by the Coordinator plugin.", StartAgent: true,
		Launch: &pluginsdk.PluginTaskLaunchOptions{AgentProfileID: &profileID},
	})
	if err != nil {
		return err
	}
	if task == nil || task.ID == "" {
		return fmt.Errorf("coordinator: host returned an empty scheduled task")
	}
	_, err = p.Host().Messages().Send(ctx, task.ID, "", prompt)
	return err
}

func dueSince(last string, now time.Time, interval time.Duration) bool {
	if last == "" {
		return true
	}
	then, err := time.Parse(time.RFC3339, last)
	return err != nil || !then.Add(interval).After(now)
}

func standupDue(last string, now time.Time, config Config) (bool, error) {
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return false, err
	}
	clock, err := time.Parse("15:04", config.StandupTime)
	if err != nil {
		return false, err
	}
	local := now.In(location)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday || local.Hour() < clock.Hour() || (local.Hour() == clock.Hour() && local.Minute() < clock.Minute()) {
		return false, nil
	}
	if last == "" {
		return true, nil
	}
	previous, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true, nil
	}
	return previous.In(location).Format("2006-01-02") != local.Format("2006-01-02"), nil
}
