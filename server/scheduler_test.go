package main

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

type schedulerFakeHost struct {
	actionFakeHost
	workspaces []pluginsdk.Workspace
	workflows  []pluginsdk.Workflow
	created    []pluginsdk.CreateTaskInput
	sent       []string
}

func (h *schedulerFakeHost) Workspaces() pluginsdk.WorkspaceReader {
	return schedulerWorkspaceReader{h}
}
func (h *schedulerFakeHost) Workflows() pluginsdk.WorkflowReader { return schedulerWorkflowReader{h} }
func (h *schedulerFakeHost) Tasks() pluginsdk.TaskReader         { return schedulerTaskReader{h} }
func (h *schedulerFakeHost) Messages() pluginsdk.MessageReader   { return schedulerMessageReader{h} }

type schedulerWorkspaceReader struct{ host *schedulerFakeHost }

func (r schedulerWorkspaceReader) List(context.Context, pluginsdk.Page) ([]pluginsdk.Workspace, *pluginsdk.PageInfo, error) {
	return r.host.workspaces, nil, nil
}

type schedulerWorkflowReader struct{ host *schedulerFakeHost }

func (r schedulerWorkflowReader) List(context.Context, string, pluginsdk.Page) ([]pluginsdk.Workflow, *pluginsdk.PageInfo, error) {
	return r.host.workflows, nil, nil
}
func (schedulerWorkflowReader) ListSteps(context.Context, string) ([]pluginsdk.WorkflowStep, error) {
	return nil, nil
}

type schedulerTaskReader struct{ host *schedulerFakeHost }

func (schedulerTaskReader) List(context.Context, pluginsdk.TaskFilter, pluginsdk.Page) ([]pluginsdk.Task, *pluginsdk.PageInfo, error) {
	return nil, nil, nil
}
func (schedulerTaskReader) Get(context.Context, string) (*pluginsdk.Task, error) { return nil, nil }
func (r schedulerTaskReader) Create(_ context.Context, input pluginsdk.CreateTaskInput) (*pluginsdk.Task, error) {
	r.host.created = append(r.host.created, input)
	return &pluginsdk.Task{ID: "scheduled-task"}, nil
}
func (schedulerTaskReader) Update(context.Context, pluginsdk.UpdateTaskInput) (*pluginsdk.Task, error) {
	return nil, nil
}

type schedulerMessageReader struct{ host *schedulerFakeHost }

func (schedulerMessageReader) List(context.Context, pluginsdk.MessageFilter, pluginsdk.Page) ([]pluginsdk.Message, *pluginsdk.PageInfo, error) {
	return nil, nil, nil
}
func (r schedulerMessageReader) Send(_ context.Context, _, _, text string) (*pluginsdk.MessageDispatch, error) {
	r.host.sent = append(r.host.sent, text)
	return &pluginsdk.MessageDispatch{}, nil
}

func newSchedulerFakeHost() *schedulerFakeHost {
	return &schedulerFakeHost{actionFakeHost: *newActionFakeHost(), workspaces: []pluginsdk.Workspace{{ID: "workspace-1"}}, workflows: []pluginsdk.Workflow{{ID: "workflow-1", WorkspaceID: "workspace-1"}}}
}

func TestDispatchDueCreatesAndPromptsConfiguredWorkstep(t *testing.T) {
	host := newSchedulerFakeHost()
	host.config = map[string]any{"agent_profile": "profile-1", "base_prompt": "base instruction", "timezone": "America/Montreal", "standup_time": "23:00", "cycle_interval_minutes": float64(45)}
	plugin := &coordinatorPlugin{}
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.saveWorkflowPolicy(context.Background(), "workspace-1", WorkflowPolicy{WorkflowID: "workflow-1", Worksteps: []WorkstepPolicy{{WorkstepID: "step-1", Prompt: "workstep instruction"}}}))

	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	require.NoError(t, plugin.dispatchDue(context.Background(), now))
	require.Len(t, host.created, 1)
	require.Equal(t, coordinatorTaskTitle, host.created[0].Title)
	require.True(t, host.created[0].StartAgent)
	require.Equal(t, "step-1", *host.created[0].WorkflowStepID)
	require.Equal(t, "profile-1", *host.created[0].Launch.AgentProfileID)
	require.Len(t, host.sent, 1)
	require.Contains(t, host.sent[0], "base instruction")
	require.Contains(t, host.sent[0], "workstep instruction")
	require.Contains(t, host.sent[0], "NON-OVERRIDABLE SAFETY INVARIANTS")
}

func TestDispatchDueCreatesDailyReportOncePerWeekday(t *testing.T) {
	host := newSchedulerFakeHost()
	host.config = map[string]any{"agent_profile": "profile-1", "timezone": "America/Montreal", "standup_time": "07:55", "cycle_interval_minutes": float64(45)}
	plugin := &coordinatorPlugin{}
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.saveWorkflowPolicy(context.Background(), "workspace-1", WorkflowPolicy{WorkflowID: "workflow-1", Worksteps: []WorkstepPolicy{{WorkstepID: "step-1"}}}))
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	require.NoError(t, plugin.dispatchDue(context.Background(), now))
	require.Len(t, host.created, 2)
	require.Equal(t, "Coordinator daily report", host.created[1].Title)
	require.Contains(t, host.sent[1], "coordinator_record_report")
	require.NoError(t, plugin.dispatchDue(context.Background(), now.Add(time.Minute)))
	require.Len(t, host.created, 2)
}

func TestDispatchDueSkipsWorkflowWithNoSelectedWorksteps(t *testing.T) {
	host := newSchedulerFakeHost()
	host.config = map[string]any{"agent_profile": "profile-1", "timezone": "America/Montreal", "standup_time": "23:00", "cycle_interval_minutes": float64(45)}
	plugin := &coordinatorPlugin{}
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.saveWorkflowPolicy(context.Background(), "workspace-1", WorkflowPolicy{WorkflowID: "workflow-1"}))
	require.NoError(t, plugin.dispatchDue(context.Background(), time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)))
	require.Empty(t, host.created)
	require.Empty(t, host.sent)
}
