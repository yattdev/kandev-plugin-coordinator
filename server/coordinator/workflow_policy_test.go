package coordinator

import (
	"context"
	"fmt"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestSelectedChecksUsePluginOwnedWorkflowPolicy(t *testing.T) {
	host := newFakeHost()
	host.workflows = []pluginsdk.Workflow{
		{ID: "workflow-b", WorkspaceID: "workspace-1", Name: "Beta"},
		{ID: "workflow-a", WorkspaceID: "workspace-1", Name: "Alpha"},
	}
	host.steps["workflow-a"] = []pluginsdk.WorkflowStep{
		{ID: "ignored", WorkflowID: "workflow-a", Name: "Todo"},
		{ID: "step-a", WorkflowID: "workflow-a", Name: "Work"},
	}
	host.steps["workflow-b"] = []pluginsdk.WorkflowStep{
		{ID: "step-b", WorkflowID: "workflow-b", Name: "Review"},
	}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.savePolicy(context.Background(), "workspace-1", []WorkflowPolicy{
		{WorkflowID: "workflow-b", WorkstepID: "step-b"},
		{WorkflowID: "workflow-a", WorkstepID: "step-a", Prompt: "check blockers"},
	}))

	checks, err := plugin.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, []PolicyCheck{
		{WorkflowID: "workflow-a", WorkflowName: "Alpha", WorkstepID: "step-a", WorkstepName: "Work", Prompt: "check blockers"},
		{WorkflowID: "workflow-b", WorkflowName: "Beta", WorkstepID: "step-b", WorkstepName: "Review"},
	}, checks)
	_, found := host.state[stateMapKey("workspace", "workspace-1", stateKeyV2)]
	require.True(t, found, "the policy is persisted under plugin-owned workspace state")
}

func TestSelectedChecksFollowWorkflowPagination(t *testing.T) {
	host := newFakeHost()
	for index := 0; index < 101; index++ {
		workflowID := fmt.Sprintf("workflow-%03d", index)
		host.workflows = append(host.workflows, pluginsdk.Workflow{ID: workflowID, WorkspaceID: "workspace-1", Name: workflowID})
	}
	host.steps["workflow-100"] = []pluginsdk.WorkflowStep{{
		ID: "step-last", WorkflowID: "workflow-100", Name: "Last",
	}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.savePolicy(context.Background(), "workspace-1", []WorkflowPolicy{{WorkflowID: "workflow-100", WorkstepID: "step-last"}}))
	checks, err := plugin.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, "workflow-100", checks[0].WorkflowID)
}

func TestWorkflowPolicyPersistsAcrossPluginRestartAndIsWorkspaceScoped(t *testing.T) {
	host := newFakeHost()
	host.workflows = []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-1", Name: "One"}, {ID: "workflow-2", WorkspaceID: "workspace-2", Name: "Two"}}
	host.steps["workflow-1"] = []pluginsdk.WorkflowStep{{ID: "step-1", WorkflowID: "workflow-1", Name: "Work"}}
	host.steps["workflow-2"] = []pluginsdk.WorkflowStep{{ID: "step-2", WorkflowID: "workflow-2", Name: "Review"}}
	first := New()
	first.UnimplementedPlugin.SetHost(host)
	require.NoError(t, first.savePolicy(context.Background(), "workspace-1", []WorkflowPolicy{{WorkflowID: "workflow-1", WorkstepID: "step-1", Prompt: "first"}}))
	require.NoError(t, first.savePolicy(context.Background(), "workspace-2", []WorkflowPolicy{{WorkflowID: "workflow-2", WorkstepID: "step-2", Prompt: "second"}}))

	restarted := New()
	restarted.UnimplementedPlugin.SetHost(host)
	checks, err := restarted.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, []PolicyCheck{{WorkflowID: "workflow-1", WorkflowName: "One", WorkstepID: "step-1", WorkstepName: "Work", Prompt: "first"}}, checks)
	policy, err := restarted.policy(context.Background(), "workspace-2")
	require.NoError(t, err)
	require.Equal(t, []WorkflowPolicy{{WorkflowID: "workflow-2", WorkstepID: "step-2", Prompt: "second"}}, policy)
}

func TestDeletedPolicySelectionRemainsSavedButIsUnavailable(t *testing.T) {
	host := newFakeHost()
	host.workflows = []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-1", Name: "One"}}
	host.steps["workflow-1"] = []pluginsdk.WorkflowStep{{ID: "step-1", WorkflowID: "workflow-1", Name: "Work"}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.savePolicy(context.Background(), "workspace-1", []WorkflowPolicy{{WorkflowID: "workflow-1", WorkstepID: "step-1"}}))
	host.steps["workflow-1"] = nil
	checks, err := plugin.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Empty(t, checks)
	policy, err := plugin.policy(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Len(t, policy, 1)
}
