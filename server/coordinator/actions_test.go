package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestEnsureActionReturnsManagedDescriptor(t *testing.T) {
	manager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{
			TaskID: "hidden-task", SessionID: "session-1", WorkspaceID: "workspace-verified", ConversationKey: ConversationKey,
		},
		ensureState: "exists",
	}
	host := newFakeHost()
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: ActionEnsure,
		Context:   pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-verified"},
		Body:      []byte(`{"workspace_id":"workspace-untrusted"}`),
	})
	require.NoError(t, err)
	var body struct {
		Status       string                 `json:"status"`
		Conversation ConversationDescriptor `json:"conversation"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &body))
	require.Equal(t, "ready", body.Status)
	require.Equal(t, "workspace-verified", body.Conversation.WorkspaceID)
	require.Equal(t, ConversationKey, body.Conversation.Key)
	require.Equal(t, "workspace-verified", manager.ensureSpecs[0].WorkspaceID)
}

func TestEnsureActionReturnsTypedConfigurationState(t *testing.T) {
	manager := &fakeSDKConversationManager{ensureState: "configuration_required"}
	host := newFakeHost()
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: ActionEnsure, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"},
	})
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body, &body))
	require.Equal(t, "configuration_required", body["status"])
	require.Empty(t, manager.ensure.TaskID)
}

func TestActionsRejectMissingVerifiedWorkspace(t *testing.T) {
	plugin := New()
	installTestPolicyStore(t, plugin)
	_, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionStatus})
	require.ErrorContains(t, err, "verified workspace context")
}

func TestPolicyActionUsesVerifiedWorkspaceAndPersistsSelections(t *testing.T) {
	host := newFakeHost()
	host.workflows = []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-verified", Name: "Build"}}
	host.steps["workflow-1"] = []pluginsdk.WorkflowStep{{ID: "step-1", WorkflowID: "workflow-1", Name: "Work"}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: ActionPolicy, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-verified"},
		Body: []byte(`{"selections":[{"workflow_id":"workflow-1","workstep_id":"step-1","prompt":"inspect blockers"}]}`),
	})
	require.NoError(t, err)
	var body struct {
		Selections []WorkflowPolicy `json:"selections"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &body))
	require.Equal(t, []WorkflowPolicy{{WorkflowID: "workflow-1", WorkstepID: "step-1", Prompt: "inspect blockers"}}, body.Selections)
	checks, err := plugin.selectedChecks(context.Background(), "workspace-verified")
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, "inspect blockers", checks[0].Prompt)
}

func TestStatusAndManualActionsReportUnavailablePolicyAsConfigurationRequired(t *testing.T) {
	host := newFakeHost()
	host.config = map[string]any{"monitoring_enabled": true}
	plugin := NewWithConversationManager(unavailableConversationManager{})
	installTestPolicyStore(t, plugin)
	plugin.UnimplementedPlugin.SetHost(host)
	status, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionStatus, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}})
	require.NoError(t, err)
	var statusBody map[string]any
	require.NoError(t, json.Unmarshal(status.Body, &statusBody))
	require.Equal(t, "configuration_required", statusBody["status"])
	manual, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionRunCycle, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}, Body: []byte(`{"idempotency_key":"manual-1"}`)})
	require.NoError(t, err)
	var manualBody map[string]any
	require.NoError(t, json.Unmarshal(manual.Body, &manualBody))
	require.Equal(t, "configuration_required", manualBody["status"])
}
