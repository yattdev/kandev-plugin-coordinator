package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func configuredScheduler(t *testing.T, dispatchStatus string) (*Plugin, *fakeHost, *fakeSDKConversationManager) {
	t.Helper()
	manager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{
			TaskID: "task-hidden", SessionID: "session-1", WorkspaceID: "workspace-1", ConversationKey: ConversationKey,
		},
		ensureState: "exists",
		dispatch: pluginsdk.AgentConversationDispatch{
			Status: dispatchStatus, SessionID: "session-1",
			Descriptor: pluginsdk.AgentConversationDescriptor{TaskID: "task-hidden", SessionID: "session-1"},
		},
	}
	host := newFakeHost()
	host.config = map[string]any{"monitoring_enabled": true}
	host.workspaces = []pluginsdk.Workspace{{ID: "workspace-1"}}
	host.workflows = []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-1", Name: "Build"}}
	host.steps["workflow-1"] = []pluginsdk.WorkflowStep{{
		ID: "step-1", WorkflowID: "workflow-1", Name: "Work",
		CoordinatorMonitored: true, CoordinatorPrompt: "check progress",
	}}
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	return plugin, host, manager
}

func TestScheduledStandupArmsCyclesAndUsesStableOccurrences(t *testing.T) {
	plugin, _, manager := configuredScheduler(t, "started")
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	config, err := plugin.config(context.Background())
	require.NoError(t, err)

	require.NoError(t, plugin.runWorkspaceDue(context.Background(), "workspace-1", config, now))
	require.Len(t, manager.dispatches, 1)
	require.Contains(t, manager.dispatches[0].text, "WAKE:STANDUP")
	require.Equal(t, "scheduled/workspace-1/standup/2026-08-17", manager.dispatches[0].occurrenceKey)

	require.NoError(t, plugin.runWorkspaceDue(context.Background(), "workspace-1", config, now.Add(time.Minute)))
	require.Len(t, manager.dispatches, 2)
	require.Contains(t, manager.dispatches[1].text, "WAKE:CYCLE")
	firstCycle := manager.dispatches[1].occurrenceKey
	require.NoError(t, plugin.runWorkspaceDue(context.Background(), "workspace-1", config, now.Add(2*time.Minute)))
	require.Len(t, manager.dispatches, 2)
	key, eligible, err := CycleOccurrenceKey("workspace-1", now.Add(2*time.Minute), config)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Equal(t, firstCycle, key)
}

func TestBusyStandupIsVisibleAndDoesNotArmCycles(t *testing.T) {
	plugin, _, _ := configuredScheduler(t, "skipped_busy")
	config, err := plugin.config(context.Background())
	require.NoError(t, err)
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	require.NoError(t, plugin.runWorkspaceDue(context.Background(), "workspace-1", config, now))

	state, err := plugin.readState(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.False(t, state.Schedule.Armed)
	require.Equal(t, "skipped_busy", state.Schedule.LastDispatch.Status)
	page, err := plugin.listReports(context.Background(), "workspace-1", "", 20)
	require.NoError(t, err)
	require.Len(t, page.Reports, 1)
	require.Equal(t, ReportStatus, page.Reports[0].Type)
	require.Contains(t, page.Reports[0].Body, "skipped_busy")
}

func TestDuplicateStandupIsVisibleAndDoesNotArmCycles(t *testing.T) {
	plugin, _, _ := configuredScheduler(t, "duplicate_occurrence")
	config, err := plugin.config(context.Background())
	require.NoError(t, err)
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	require.NoError(t, plugin.runWorkspaceDue(context.Background(), "workspace-1", config, now))

	state, err := plugin.readState(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.False(t, state.Schedule.Armed)
	require.Equal(t, "duplicate_occurrence", state.Schedule.LastDispatch.Status)
	page, err := plugin.listReports(context.Background(), "workspace-1", "", 20)
	require.NoError(t, err)
	require.Len(t, page.Reports, 1)
	require.Equal(t, ReportStatus, page.Reports[0].Type)
	require.Contains(t, page.Reports[0].Body, "duplicate_occurrence")
}

func TestManualRunsHaveSeparateCallerIdempotency(t *testing.T) {
	plugin, _, manager := configuredScheduler(t, "started")
	_, err := plugin.RunManual(context.Background(), "workspace-1", TriggerCycle, "button-1")
	require.NoError(t, err)
	require.Equal(t, "manual/workspace-1/cycle/button-1", manager.dispatches[0].occurrenceKey)
	require.Error(t, func() error {
		_, err := plugin.RunManual(context.Background(), "workspace-1", TriggerCycle, "")
		return err
	}())
	require.ErrorContains(t, func() error {
		_, err := plugin.RunManual(context.Background(), "workspace-1", TriggerCycle, string(make([]byte, 257)))
		return err
	}(), "must not exceed")
}

func TestManualBusyDispatchCreatesStatusWithoutArmingSchedule(t *testing.T) {
	plugin, _, _ := configuredScheduler(t, "skipped_busy")
	result, err := plugin.RunManual(context.Background(), "workspace-1", TriggerStandup, "button-1")
	require.NoError(t, err)
	require.Equal(t, "skipped_busy", result.Status)

	state, err := plugin.readState(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.False(t, state.Schedule.Armed, "a manual standup must not arm scheduled cycles")
	require.Equal(t, "skipped_busy", state.Schedule.LastDispatch.Status)
	page, err := plugin.listReports(context.Background(), "workspace-1", "", 20)
	require.NoError(t, err)
	require.Len(t, page.Reports, 1)
	require.Equal(t, ReportStatus, page.Reports[0].Type)
}

func TestRunnerStartsOnceAndStops(t *testing.T) {
	host := newFakeHost()
	host.config = map[string]any{"monitoring_enabled": false}
	plugin := NewWithConversationManager(unavailableConversationManager{})
	plugin.tickInterval = time.Hour
	plugin.SetHost(host)
	firstDone := plugin.runnerDone
	plugin.SetHost(host)
	require.Equal(t, firstDone, plugin.runnerDone)
	plugin.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}

func TestCycleOccurrenceUsesStableCivilSlotsAcrossDSTFallback(t *testing.T) {
	config, err := ConfigFrom(map[string]any{
		"schedule_days": "every_day", "monitoring_window_start": "00:00",
		"monitoring_window_end": "03:00", "cycle_interval_minutes": 45,
	})
	require.NoError(t, err)

	first, eligible, err := CycleOccurrenceKey("workspace-1", time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), config)
	require.NoError(t, err)
	require.True(t, eligible)
	second, eligible, err := CycleOccurrenceKey("workspace-1", time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), config)
	require.NoError(t, err)
	require.True(t, eligible)
	require.Equal(t, first, second, "repeated local time must map to the same cadence slot")
}
