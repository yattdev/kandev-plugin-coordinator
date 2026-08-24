package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceCoordinatorLedgerSurvivesExecutionReplacement(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	plugin.nowFn = func() time.Time { return now }

	_, state, err := plugin.publishReport(context.Background(), "workspace-1", PublishReportInput{
		Type: ReportCycle, Title: "Reconciliation", Body: "Checked the board.",
		State: PublishedState{
			Runs:      []CoordinatorRun{{ID: "run-1", OccurrenceKey: "automation-1", Status: "completed", StartedAt: now.Format(time.RFC3339Nano), SessionID: "session-old", TaskID: "hidden-old"}},
			FollowUps: []FollowUp{{ID: "follow-up-1", TargetTaskID: "task-1", Request: "Provide CI receipt", ExpectedEvidence: "current-head CI URL", SentAt: now.Format(time.RFC3339Nano), AttemptCount: 1, Status: "pending", Fallback: "raise human decision"}},
			Inbox:     []InboxItem{{ID: "inbox-1", Kind: "human_decision", TaskID: "task-1", Title: "Approve release scope", CreatedAt: now.Format(time.RFC3339Nano), Status: "open"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, ConversationKey, state.Identity.LogicalKey)
	require.Equal(t, "session-old", state.Runs[0].SessionID)
	require.Equal(t, "pending", state.FollowUps[0].Status)
	require.Equal(t, "open", state.Inbox[0].Status)

	// A new execution can replace the underlying task/session while the logical
	// Coordinator identity and previous follow-up/inbox state remain durable.
	updated, err := plugin.updateDocument(context.Background(), "workspace-1", func(doc *workspaceDocument) error {
		doc.State.Runs = append([]CoordinatorRun{{ID: "run-2", Status: "running", StartedAt: now.Add(time.Minute).Format(time.RFC3339Nano), SessionID: "session-new", TaskID: "hidden-new"}}, doc.State.Runs...)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, ConversationKey, updated.State.Identity.LogicalKey)
	require.Equal(t, "session-new", updated.State.Runs[0].SessionID)
	require.Equal(t, "session-old", updated.State.Runs[1].SessionID)
	require.Len(t, updated.State.FollowUps, 1)
	require.Len(t, updated.State.Inbox, 1)

	_, preserved, err := plugin.publishReport(context.Background(), "workspace-1", PublishReportInput{
		Type: ReportStatus, Title: "Status", Body: "No ledger update.", State: PublishedState{},
	})
	require.NoError(t, err)
	require.Len(t, preserved.Runs, 2)
	require.Len(t, preserved.FollowUps, 1)
	require.Len(t, preserved.Inbox, 1)
}

func TestCoordinatorCapabilityDegradationIsTypedAndHostBound(t *testing.T) {
	doc := emptyDocument()
	require.Equal(t, "unavailable", doc.State.Capabilities.Principal.Status)
	require.Equal(t, "unavailable", doc.State.Capabilities.Inbox.Status)
	require.Equal(t, "unavailable", doc.State.Capabilities.Automations.Status)
	require.Equal(t, "available", doc.State.Capabilities.Relations.Status)
	require.NotEmpty(t, doc.State.Capabilities.Principal.Reason)
}

func TestCoordinatorLedgerRejectsInvalidTypedEntries(t *testing.T) {
	_, _, err := New().publishReport(context.Background(), "workspace-1", PublishReportInput{
		Type: ReportStatus, Title: "Invalid", Body: "Invalid ledger entry",
		State: PublishedState{FollowUps: []FollowUp{{ID: "follow-up", Request: "missing required fields", Status: "pending"}}},
	})
	require.ErrorContains(t, err, "invalid coordinator follow-up")
}
