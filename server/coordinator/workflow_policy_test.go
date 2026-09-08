package coordinator

import (
	"context"
	"fmt"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestSelectedChecksUseHostOwnedWorkflowStepPolicy(t *testing.T) {
	host := newFakeHost()
	host.workflows = []pluginsdk.Workflow{
		{ID: "workflow-b", WorkspaceID: "workspace-1", Name: "Beta"},
		{ID: "workflow-a", WorkspaceID: "workspace-1", Name: "Alpha"},
	}
	host.steps["workflow-a"] = []pluginsdk.WorkflowStep{
		{ID: "ignored", WorkflowID: "workflow-a", Name: "Todo"},
		{ID: "step-a", WorkflowID: "workflow-a", Name: "Work", CoordinatorMonitored: true, CoordinatorPrompt: "check blockers"},
	}
	host.steps["workflow-b"] = []pluginsdk.WorkflowStep{
		{ID: "step-b", WorkflowID: "workflow-b", Name: "Review", CoordinatorMonitored: true},
	}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)

	checks, err := plugin.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, []PolicyCheck{
		{WorkflowID: "workflow-a", WorkflowName: "Alpha", WorkstepID: "step-a", WorkstepName: "Work", Prompt: "check blockers"},
		{WorkflowID: "workflow-b", WorkflowName: "Beta", WorkstepID: "step-b", WorkstepName: "Review"},
	}, checks)
	_, found := host.state[stateMapKey("workspace", "workspace-1", "workflow_policy:workflow-a")]
	require.False(t, found, "the plugin must not shadow host-owned workflow policy")
}

func TestSelectedChecksFollowWorkflowPagination(t *testing.T) {
	host := newFakeHost()
	for index := 0; index < 101; index++ {
		workflowID := fmt.Sprintf("workflow-%03d", index)
		host.workflows = append(host.workflows, pluginsdk.Workflow{ID: workflowID, WorkspaceID: "workspace-1", Name: workflowID})
	}
	host.steps["workflow-100"] = []pluginsdk.WorkflowStep{{
		ID: "step-last", WorkflowID: "workflow-100", Name: "Last", CoordinatorMonitored: true,
	}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	checks, err := plugin.selectedChecks(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, "workflow-100", checks[0].WorkflowID)
}
