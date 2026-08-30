package main

import (
	"context"
	"strconv"
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

func (r schedulerWorkspaceReader) List(_ context.Context, page pluginsdk.Page) ([]pluginsdk.Workspace, *pluginsdk.PageInfo, error) {
	return paginateTestItems(r.host.workspaces, page)
}

func paginateTestItems[T any](items []T, page pluginsdk.Page) ([]T, *pluginsdk.PageInfo, error) {
	limit := int(page.Limit)
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	start := 0
	if page.Cursor != "" {
		parsed, err := strconv.Atoi(page.Cursor)
		if err != nil {
			return nil, nil, err
		}
		start = parsed
	}
	if start >= len(items) {
		return nil, &pluginsdk.PageInfo{}, nil
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	info := &pluginsdk.PageInfo{}
	if end < len(items) {
		info.HasMore = true
		info.NextCursor = strconv.Itoa(end)
	}
	return items[start:end], info, nil
}

type schedulerWorkflowReader struct{ host *schedulerFakeHost }

func (r schedulerWorkflowReader) List(_ context.Context, workspaceID string, page pluginsdk.Page) ([]pluginsdk.Workflow, *pluginsdk.PageInfo, error) {
	var workflows []pluginsdk.Workflow
	for _, workflow := range r.host.workflows {
		if workflow.WorkspaceID == "" || workflow.WorkspaceID == workspaceID {
			workflows = append(workflows, workflow)
		}
	}
	return paginateTestItems(workflows, page)
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
	require.NotNil(t, host.created[0].Launch.Prompt)
	require.Contains(t, *host.created[0].Launch.Prompt, "base instruction")
	require.Contains(t, *host.created[0].Launch.Prompt, "workstep instruction")
	require.Contains(t, *host.created[0].Launch.Prompt, "NON-OVERRIDABLE SAFETY INVARIANTS")
	require.Empty(t, host.sent)
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
	require.NotNil(t, host.created[1].Launch.Prompt)
	require.Contains(t, *host.created[1].Launch.Prompt, "coordinator_record_report")
	require.Empty(t, host.sent)
	require.NoError(t, plugin.dispatchDue(context.Background(), now.Add(time.Minute)))
	require.Len(t, host.created, 2)
}

func TestDispatchDueFollowsWorkspaceAndWorkflowPagination(t *testing.T) {
	host := newSchedulerFakeHost()
	host.config = map[string]any{"agent_profile": "profile-1", "timezone": "America/Montreal", "standup_time": "23:00", "cycle_interval_minutes": float64(45)}
	host.workspaces = make([]pluginsdk.Workspace, 101)
	for i := range host.workspaces {
		host.workspaces[i] = pluginsdk.Workspace{ID: "workspace-ignored"}
	}
	host.workspaces[100] = pluginsdk.Workspace{ID: "workspace-2"}
	host.workflows = make([]pluginsdk.Workflow, 101)
	for i := range host.workflows {
		host.workflows[i] = pluginsdk.Workflow{ID: "workflow-ignored", WorkspaceID: "workspace-2"}
	}
	host.workflows[100] = pluginsdk.Workflow{ID: "workflow-2", WorkspaceID: "workspace-2"}
	plugin := &coordinatorPlugin{}
	plugin.UnimplementedPlugin.SetHost(host)
	require.NoError(t, plugin.saveWorkflowPolicy(context.Background(), "workspace-2", WorkflowPolicy{WorkflowID: "workflow-2", Worksteps: []WorkstepPolicy{{WorkstepID: "step-2", Prompt: "second page"}}}))
	require.NoError(t, plugin.dispatchDue(context.Background(), time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)))
	require.Len(t, host.created, 1)
	require.Equal(t, "workspace-2", host.created[0].WorkspaceID)
	require.Equal(t, "workflow-2", host.created[0].WorkflowID)
	require.Equal(t, "step-2", *host.created[0].WorkflowStepID)
	require.Contains(t, *host.created[0].Launch.Prompt, "second page")
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
