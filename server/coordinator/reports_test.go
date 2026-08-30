package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestPublishReportAtomicallyUpdatesBoundedWorkspaceState(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	plugin.nowFn = func() time.Time { return base }

	artifact, state, err := plugin.publishReport(context.Background(), "workspace-1", PublishReportInput{
		Type: ReportDaily, Title: "Daily", Body: "All clear",
		State: PublishedState{
			ActiveFlags:   []ActiveFlag{{TaskID: "task-1", Reason: "decision", FlaggedAt: base.Format(time.RFC3339Nano)}},
			TaskSnapshots: map[string]TaskActivitySnapshot{"task-1": {TaskID: "task-1", Classification: "blocked-or-flagged"}},
			Degradations:  []string{"native flags unavailable"},
			CycleLog:      &CycleLog{At: base.Format(time.RFC3339Nano), Summary: "checked 1"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, artifact.CreatedAt, state.LastReportAt)
	require.Equal(t, "task-1", state.ActiveFlags[0].TaskID)

	page, err := plugin.listReports(context.Background(), "workspace-1", "", 20)
	require.NoError(t, err)
	require.Equal(t, []ReportArtifact{artifact}, page.Reports)
	other, err := plugin.listReports(context.Background(), "workspace-2", "", 20)
	require.NoError(t, err)
	require.Empty(t, other.Reports)
}

func TestReportValidationAndCursorPagination(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	plugin.nowFn = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }
	_, _, err := plugin.publishReport(context.Background(), "workspace-1", PublishReportInput{
		Type: ReportDaily, Title: "too large", Body: strings.Repeat("x", maxReportBytes), State: PublishedState{},
	})
	require.ErrorContains(t, err, "exceeds")

	for index := 0; index < 3; index++ {
		plugin.nowFn = func() time.Time { return time.Date(2026, 8, 20, 12, index, 0, 0, time.UTC) }
		_, _, err = plugin.publishReport(context.Background(), "workspace-1", PublishReportInput{
			Type: ReportCycle, Title: "Cycle", Body: "body", State: PublishedState{},
		})
		require.NoError(t, err)
	}
	first, err := plugin.listReports(context.Background(), "workspace-1", "", 2)
	require.NoError(t, err)
	require.Len(t, first.Reports, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := plugin.listReports(context.Background(), "workspace-1", first.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, second.Reports, 1)
	require.Empty(t, second.NextCursor)
	_, err = plugin.listReports(context.Background(), "workspace-1", "not-base64!", 2)
	require.ErrorContains(t, err, "invalid report cursor")
}

func TestAgentToolsUseVerifiedWorkspaceContext(t *testing.T) {
	host := newFakeHost()
	manager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{
			TaskID: "coordinator-task", SessionID: "coordinator-session",
			WorkspaceID: "workspace-verified", ConversationKey: ConversationKey,
		},
		ensureState: "exists",
	}
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	plugin.nowFn = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }

	result, err := plugin.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{
		Name: ToolPublishReport,
		Context: pluginsdk.AgentToolContext{
			WorkspaceID: "workspace-verified", TaskID: "coordinator-task", SessionID: "coordinator-session",
		},
		Arguments: map[string]any{
			"type": "status", "title": "Status", "body": "ok",
			"state":        map[string]any{"task_snapshots": map[string]any{}},
			"workspace_id": "workspace-untrusted",
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	_, found, err := host.GetState(context.Background(), "workspace", "workspace-verified", stateKeyV2)
	require.NoError(t, err)
	require.True(t, found)
	_, found, err = host.GetState(context.Background(), "workspace", "workspace-untrusted", stateKeyV2)
	require.NoError(t, err)
	require.False(t, found)

	stateResult, err := plugin.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{
		Name: ToolGetState, Context: pluginsdk.AgentToolContext{
			WorkspaceID: "workspace-verified", TaskID: "coordinator-task", SessionID: "coordinator-session",
		},
	})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(stateResult.Text), &decoded))
	require.Contains(t, decoded, "state")
	require.Equal(t, decoded, stateResult.StructuredContent)

	var reportDecoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Text), &reportDecoded))
	require.Equal(t, reportDecoded, result.StructuredContent)
}

func TestAgentToolsRejectOtherSessionsInTheSameWorkspace(t *testing.T) {
	tests := []struct{ name, taskID, sessionID string }{
		{name: "different task", taskID: "ordinary-task", sessionID: "coordinator-session"},
		{name: "different session", taskID: "coordinator-task", sessionID: "ordinary-session"},
		{name: "missing identities"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := newFakeHost()
			manager := &fakeSDKConversationManager{
				ensure: pluginsdk.AgentConversationDescriptor{
					TaskID: "coordinator-task", SessionID: "coordinator-session",
					WorkspaceID: "workspace-1", ConversationKey: ConversationKey,
				},
				ensureState: "exists",
			}
			plugin := NewWithConversationManager(hostConversationManager{manager: manager})
			plugin.UnimplementedPlugin.SetHost(host)

			result, err := plugin.InvokeAgentTool(context.Background(), &pluginsdk.AgentToolRequest{
				Name: ToolPublishReport,
				Context: pluginsdk.AgentToolContext{
					WorkspaceID: "workspace-1", TaskID: test.taskID, SessionID: test.sessionID,
				},
				Arguments: map[string]any{
					"type": "status", "title": "Forged", "body": "not allowed",
					"state": map[string]any{"task_snapshots": map[string]any{}},
				},
			})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Contains(t, result.Text, "managed coordinator conversation")
			_, found, err := host.GetState(context.Background(), "workspace", "workspace-1", stateKeyV2)
			require.NoError(t, err)
			require.False(t, found)
		})
	}
}

func TestWorkspaceHistoryIsBoundedAndOldLogsCompact(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	plugin.nowFn = func() time.Time { return now }
	doc := emptyDocument()
	for index := 0; index < MaxReports+5; index++ {
		doc.Reports = append(doc.Reports, ReportArtifact{ID: string(rune('a' + index%26)), Type: ReportCycle, Title: "Cycle", Body: "body", CreatedAt: now.Format(time.RFC3339Nano)})
	}
	doc.State.CycleLogs = []CycleLog{
		{At: now.Add(-14 * 24 * time.Hour).Format(time.RFC3339Nano), Summary: "old one"},
		{At: now.Add(-13 * 24 * time.Hour).Format(time.RFC3339Nano), Summary: "old two"},
		{At: now.Add(-time.Hour).Format(time.RFC3339Nano), Summary: "recent"},
	}
	require.NoError(t, plugin.saveDocument(context.Background(), "workspace-1", doc))
	updated, err := plugin.updateDocument(context.Background(), "workspace-1", func(*workspaceDocument) error { return nil })
	require.NoError(t, err)
	require.Len(t, updated.Reports, MaxReports)
	require.LessOrEqual(t, len(updated.State.CycleLogs), 3)
	require.Contains(t, updated.State.CycleLogs[len(updated.State.CycleLogs)-1].Summary, "compacted")
}
