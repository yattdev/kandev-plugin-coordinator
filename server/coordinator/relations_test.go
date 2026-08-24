package coordinator

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestInspectTaskRelationsUsesVerifiedWorkspaceScope(t *testing.T) {
	host := newFakeHost()
	host.relations["workspace-1/task-1"] = &pluginsdk.TaskRelations{
		Task:     pluginsdk.RelationTask{ID: "task-1", WorkspaceID: "workspace-1", Identifier: "QA-1", Title: "Synthetic task", State: "open"},
		Blockers: []pluginsdk.RelationTask{{ID: "task-2", WorkspaceID: "workspace-1", Identifier: "QA-2", Title: "Synthetic blocker", State: "open"}},
	}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)

	relations, err := plugin.inspectTaskRelations(context.Background(), "workspace-1", "task-1")
	require.NoError(t, err)
	require.Equal(t, "task-1", relations.Task.ID)
	require.Equal(t, "workspace-1", host.relationWorkspaceID)
	require.Equal(t, "task-1", host.relationTaskID)
	require.Len(t, relations.Blockers, 1)
}

func TestInspectTaskRelationsRejectsMissingIdentifiers(t *testing.T) {
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(newFakeHost())
	_, err := plugin.inspectTaskRelations(context.Background(), "workspace-1", " ")
	require.ErrorContains(t, err, "workspace and task identifiers are required")
}
