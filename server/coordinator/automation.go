package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const automationTriggeredEvent = "automation.triggered"

// OnEvent handles only a server-stamped Automation event. Binding, policy, and
// prompt composition are plugin-owned; schedules and event emission stay host-owned.
func (p *Plugin) OnEvent(ctx context.Context, event *pluginsdk.Event) error {
	if event == nil || event.EventType != automationTriggeredEvent || event.WorkspaceID == "" {
		return nil
	}
	automationID, _ := event.Payload["automation_id"].(string)
	if strings.TrimSpace(automationID) == "" {
		return nil
	}
	binding, found, err := p.findAutomationBinding(ctx, event.WorkspaceID, automationID)
	if err != nil || !found {
		return err
	}
	automation, err := p.Host().Automations().Get(ctx, event.WorkspaceID, binding.AutomationID)
	if err != nil {
		return err // retain host retry for transient descriptor failures
	}
	if automation == nil || automation.WorkspaceID != event.WorkspaceID || !automation.Enabled {
		return p.recordAutomationStatus(ctx, event.WorkspaceID, event.EventID, "blocked", "Automation is unavailable or disabled.")
	}
	config, err := p.config(ctx)
	if err != nil {
		return err
	}
	if automation.AgentProfileID != "" {
		config.AgentProfile = automation.AgentProfileID
	}
	checks, err := p.selectedChecks(ctx, event.WorkspaceID)
	if err != nil {
		return err
	}
	base := config.BasePrompt
	if strings.TrimSpace(automation.Prompt) != "" {
		base += "\n\nAutomation instruction:\n" + automation.Prompt
	}
	descriptor, err := ensureConversation(ctx, p.manager, event.WorkspaceID, config)
	if err != nil {
		_ = p.recordAutomationStatus(ctx, event.WorkspaceID, event.EventID, "blocked", err.Error())
		return err
	}
	occurrence := "automation:" + automationID + ":" + event.EventID
	if dedup, _ := event.Payload["dedup_key"].(string); strings.TrimSpace(dedup) != "" {
		occurrence = "automation:" + automationID + ":" + dedup
	}
	prompt := ComposeOccurrencePrompt(base, checks, automationTrigger(automation), config.ReportTemplate)
	result, dispatchErr := p.manager.Dispatch(ctx, DispatchRequest{WorkspaceID: event.WorkspaceID, Key: ConversationKey, OccurrenceKey: occurrence, Prompt: prompt})
	status := normalizeRunStatus(result.Status)
	if err := p.recordAutomationRun(ctx, event.WorkspaceID, CoordinatorRun{ID: occurrence, OccurrenceKey: occurrence, Status: status, StartedAt: event.OccurredAt, SessionID: result.SessionID, TaskID: descriptor.TaskID}); err != nil {
		return err
	}
	if dispatchErr != nil {
		return dispatchErr
	}
	if status == "coalesced" {
		return p.recordAutomationStatus(ctx, event.WorkspaceID, occurrence, status, result.Status)
	}
	return nil
}

func (p *Plugin) bindAutomations(ctx context.Context, workspaceID string, ids []string) ([]AutomationBinding, error) {
	if len(ids) > 20 {
		return nil, fmt.Errorf("coordinator: at most 20 Automations may be bound")
	}
	seen, bindings := map[string]struct{}{}, make([]AutomationBinding, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("coordinator: automation id is required")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("coordinator: duplicate automation id")
		}
		seen[id] = struct{}{}
		automation, err := p.Host().Automations().Get(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if automation == nil || automation.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("coordinator: automation not found")
		}
		bindings = append(bindings, AutomationBinding{AutomationID: automation.ID, Name: automation.Name, BoundAt: p.now().UTC().Format(time.RFC3339Nano)})
	}
	_, err := p.updateDocument(ctx, workspaceID, func(doc *workspaceDocument) error { doc.State.AutomationBindings = bindings; return nil })
	return bindings, err
}

func (p *Plugin) findAutomationBinding(ctx context.Context, workspaceID, id string) (AutomationBinding, bool, error) {
	state, err := p.readState(ctx, workspaceID)
	if err != nil {
		return AutomationBinding{}, false, err
	}
	for _, binding := range state.AutomationBindings {
		if binding.AutomationID == id {
			return binding, true, nil
		}
	}
	return AutomationBinding{}, false, nil
}

func (p *Plugin) recordAutomationRun(ctx context.Context, workspaceID string, run CoordinatorRun) error {
	_, err := p.updateDocument(ctx, workspaceID, func(doc *workspaceDocument) error {
		for i, current := range doc.State.Runs {
			if current.ID == run.ID {
				doc.State.Runs[i] = run
				return nil
			}
		}
		doc.State.Runs = append([]CoordinatorRun{run}, doc.State.Runs...)
		return nil
	})
	return err
}

func (p *Plugin) recordAutomationStatus(ctx context.Context, workspaceID, occurrence, status, body string) error {
	if err := p.recordAutomationRun(ctx, workspaceID, CoordinatorRun{ID: occurrence, OccurrenceKey: occurrence, Status: normalizeRunStatus(status), StartedAt: p.now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	_, _, err := p.publishReport(ctx, workspaceID, PublishReportInput{Type: ReportStatus, Title: "Automation " + status, Body: body, OccurrenceKey: occurrence, State: PublishedState{}})
	return err
}

func automationTrigger(automation *pluginsdk.Automation) string {
	if automation != nil && strings.Contains(strings.ToLower(automation.Name+" "+automation.Description), "standup") {
		return TriggerStandup
	}
	return TriggerCycle
}
func normalizeRunStatus(status string) string {
	switch status {
	case "started", "sent", "queued":
		return "running"
	case "skipped_busy", "duplicate_occurrence", "coalesced":
		return "coalesced"
	case "blocked", "failed", "completed", "running":
		return status
	}
	return "failed"
}
