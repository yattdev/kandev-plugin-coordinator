package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestAutomationBindingIsWorkspaceScopedAndVerified(t *testing.T) {
	host := newFakeHost()
	host.automations["auto-1"] = &pluginsdk.Automation{ID: "auto-1", WorkspaceID: "workspace-1", Name: "Board reconciliation", Enabled: true}
	host.automations["auto-foreign"] = &pluginsdk.Automation{ID: "auto-foreign", WorkspaceID: "workspace-2", Name: "Foreign", Enabled: true}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	bindings, err := plugin.bindAutomations(context.Background(), "workspace-1", []string{"auto-1"})
	require.NoError(t, err)
	require.Equal(t, "auto-1", bindings[0].AutomationID)
	_, err = plugin.bindAutomations(context.Background(), "workspace-1", []string{"auto-foreign"})
	require.ErrorContains(t, err, "not found")
	_, err = plugin.bindAutomations(context.Background(), "workspace-1", []string{"auto-1", "auto-1"})
	require.ErrorContains(t, err, "duplicate")
}

func TestAutomationEventDispatchesOneBoundedWorkspaceOccurrence(t *testing.T) {
	manager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{WorkspaceID: "workspace-1", ConversationKey: ConversationKey, TaskID: "hidden-task", SessionID: "session-1"}, ensureState: "exists",
		dispatch: pluginsdk.AgentConversationDispatch{Status: "started", Descriptor: pluginsdk.AgentConversationDescriptor{TaskID: "hidden-task"}, SessionID: "session-1"},
	}
	host := newFakeHost()
	host.automations["auto-1"] = &pluginsdk.Automation{ID: "auto-1", WorkspaceID: "workspace-1", Name: "Daily standup", Prompt: "Use the automation template.", AgentProfileID: "profile-automation", Enabled: true}
	host.workflows = []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-1", Name: "Kanban"}}
	host.steps["workflow-1"] = []pluginsdk.WorkflowStep{{ID: "step-1", WorkflowID: "workflow-1", Name: "Review", CoordinatorMonitored: true, CoordinatorPrompt: "Check the review lane."}}
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	plugin.nowFn = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	_, err := plugin.bindAutomations(context.Background(), "workspace-1", []string{"auto-1"})
	require.NoError(t, err)

	err = plugin.OnEvent(context.Background(), &pluginsdk.Event{EventType: automationTriggeredEvent, EventID: "event-1", OccurredAt: "2026-08-24T12:00:00Z", WorkspaceID: "workspace-1", Payload: map[string]any{"automation_id": "auto-1", "workspace_id": "workspace-untrusted", "dedup_key": "dedup-1"}})
	require.NoError(t, err)
	require.Len(t, manager.dispatches, 1)
	require.Equal(t, "workspace-1", manager.dispatches[0].workspaceID)
	require.Equal(t, "automation:auto-1:dedup-1", manager.dispatches[0].occurrenceKey)
	require.Contains(t, manager.dispatches[0].text, "WAKE:STANDUP")
	require.Contains(t, manager.dispatches[0].text, "Use the automation template.")
	require.Equal(t, "profile-automation", manager.ensureSpecs[0].AgentProfileID)
	state, err := plugin.readState(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, "running", state.Runs[0].Status)
}

func TestAutomationEventIgnoresUnboundDelivery(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	err := plugin.OnEvent(context.Background(), &pluginsdk.Event{EventType: automationTriggeredEvent, EventID: "event-1", WorkspaceID: "workspace-1", Payload: map[string]any{"automation_id": "auto-unbound"}})
	require.NoError(t, err)
}
