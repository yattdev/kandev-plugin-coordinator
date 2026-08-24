package coordinator

import (
	"context"
	"strconv"
	"sync"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type fakeHost struct {
	pluginsdk.UnimplementedHostData
	mu                  sync.Mutex
	state               map[string]map[string]any
	config              map[string]any
	workspaces          []pluginsdk.Workspace
	workflows           []pluginsdk.Workflow
	steps               map[string][]pluginsdk.WorkflowStep
	automations         map[string]*pluginsdk.Automation
	principal           *pluginsdk.WorkspaceAgentPrincipal
	principalStatus     *pluginsdk.WorkspaceAgentPrincipalStatus
	relations           map[string]*pluginsdk.TaskRelations
	relationWorkspaceID string
	relationTaskID      string
	conversations       pluginsdk.AgentConversationManager
}

func newFakeHost() *fakeHost {
	return &fakeHost{state: map[string]map[string]any{}, steps: map[string][]pluginsdk.WorkflowStep{}, relations: map[string]*pluginsdk.TaskRelations{}, automations: map[string]*pluginsdk.Automation{}}
}

func stateMapKey(scope, id, key string) string { return scope + "/" + id + "/" + key }

func (h *fakeHost) GetState(_ context.Context, scope, id, key string) (map[string]any, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	value, found := h.state[stateMapKey(scope, id, key)]
	return value, found, nil
}

func (h *fakeHost) SetState(_ context.Context, scope, id, key string, value map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state[stateMapKey(scope, id, key)] = value
	return nil
}

func (h *fakeHost) DeleteState(_ context.Context, scope, id, key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, stateMapKey(scope, id, key))
	return nil
}

func (*fakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}

func (h *fakeHost) GetConfig(context.Context) (map[string]any, error)  { return h.config, nil }
func (*fakeHost) RevealSecret(context.Context, string) (string, error) { return "", nil }
func (*fakeHost) GetSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (*fakeHost) SetSecret(context.Context, string, string) error         { return nil }
func (*fakeHost) DeleteSecret(context.Context, string) error              { return nil }
func (*fakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

func (h *fakeHost) Workspaces() pluginsdk.WorkspaceReader { return fakeWorkspaceReader{host: h} }
func (h *fakeHost) Workflows() pluginsdk.WorkflowReader   { return fakeWorkflowReader{host: h} }
func (h *fakeHost) TaskRelations() pluginsdk.TaskRelationsReader {
	return fakeTaskRelationsReader{host: h}
}
func (h *fakeHost) Automations() pluginsdk.AutomationReader { return fakeAutomationReader{host: h} }
func (h *fakeHost) WorkspaceAgentPrincipals() pluginsdk.WorkspaceAgentPrincipalReader {
	return fakePrincipalReader{host: h}
}
func (h *fakeHost) AgentConversations() pluginsdk.AgentConversationManager {
	return h.conversations
}

type fakeWorkspaceReader struct{ host *fakeHost }

func (r fakeWorkspaceReader) List(_ context.Context, page pluginsdk.Page) ([]pluginsdk.Workspace, *pluginsdk.PageInfo, error) {
	return paginate(r.host.workspaces, page)
}

type fakeWorkflowReader struct{ host *fakeHost }

type fakeTaskRelationsReader struct{ host *fakeHost }

type fakeAutomationReader struct{ host *fakeHost }
type fakePrincipalReader struct{ host *fakeHost }

func (r fakePrincipalReader) Get(_ context.Context, workspaceID, key string) (*pluginsdk.WorkspaceAgentPrincipal, error) {
	if r.host.principal == nil || r.host.principal.WorkspaceID != workspaceID || r.host.principal.LogicalKey != key {
		return nil, nil
	}
	copy := *r.host.principal
	return &copy, nil
}
func (r fakePrincipalReader) Status(_ context.Context, _ string, _ string) (*pluginsdk.WorkspaceAgentPrincipalStatus, error) {
	if r.host.principalStatus == nil {
		return nil, nil
	}
	copy := *r.host.principalStatus
	return &copy, nil
}
func (r fakePrincipalReader) ListAudit(context.Context, string, string, pluginsdk.Page) ([]pluginsdk.WorkspaceAgentPrincipalAuditEvent, *pluginsdk.PageInfo, error) {
	return nil, &pluginsdk.PageInfo{}, nil
}

func (r fakeAutomationReader) List(_ context.Context, workspaceID string, _ pluginsdk.Page) ([]pluginsdk.Automation, *pluginsdk.PageInfo, error) {
	items := make([]pluginsdk.Automation, 0)
	for _, automation := range r.host.automations {
		if automation.WorkspaceID == workspaceID {
			items = append(items, *automation)
		}
	}
	return items, &pluginsdk.PageInfo{}, nil
}
func (r fakeAutomationReader) Get(_ context.Context, workspaceID, id string) (*pluginsdk.Automation, error) {
	automation := r.host.automations[id]
	if automation == nil || automation.WorkspaceID != workspaceID {
		return nil, nil
	}
	copy := *automation
	return &copy, nil
}

func (r fakeTaskRelationsReader) Get(_ context.Context, workspaceID, taskID string) (*pluginsdk.TaskRelations, error) {
	r.host.relationWorkspaceID = workspaceID
	r.host.relationTaskID = taskID
	return r.host.relations[workspaceID+"/"+taskID], nil
}

func (r fakeWorkflowReader) List(_ context.Context, workspaceID string, page pluginsdk.Page) ([]pluginsdk.Workflow, *pluginsdk.PageInfo, error) {
	items := make([]pluginsdk.Workflow, 0, len(r.host.workflows))
	for _, workflow := range r.host.workflows {
		if workflow.WorkspaceID == workspaceID {
			items = append(items, workflow)
		}
	}
	return paginate(items, page)
}

func (r fakeWorkflowReader) ListSteps(_ context.Context, workflowID string) ([]pluginsdk.WorkflowStep, error) {
	return append([]pluginsdk.WorkflowStep(nil), r.host.steps[workflowID]...), nil
}

func paginate[T any](items []T, page pluginsdk.Page) ([]T, *pluginsdk.PageInfo, error) {
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
	limit := int(page.Limit)
	if limit <= 0 || limit > len(items)-start {
		limit = len(items) - start
	}
	end := start + limit
	info := &pluginsdk.PageInfo{}
	if end < len(items) {
		info.HasMore = true
		info.NextCursor = strconv.Itoa(end)
	}
	return append([]T(nil), items[start:end]...), info, nil
}

var _ pluginsdk.Host = (*fakeHost)(nil)
var _ pluginsdk.AgentConversationHost = (*fakeHost)(nil)

type fakeSDKConversationManager struct {
	mu          sync.Mutex
	ensureSpecs []pluginsdk.AgentConversationSpec
	dispatches  []fakeDispatch
	ensure      pluginsdk.AgentConversationDescriptor
	ensureState string
	dispatch    pluginsdk.AgentConversationDispatch
	ensureErr   error
	dispatchErr error
}

type fakeDispatch struct {
	workspaceID, conversationKey, text, occurrenceKey string
}

func (m *fakeSDKConversationManager) Ensure(_ context.Context, spec pluginsdk.AgentConversationSpec) (pluginsdk.AgentConversationDescriptor, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureSpecs = append(m.ensureSpecs, spec)
	return m.ensure, m.ensureState, m.ensureErr
}

func (m *fakeSDKConversationManager) Dispatch(_ context.Context, workspaceID, conversationKey, text, occurrenceKey string) (pluginsdk.AgentConversationDispatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatches = append(m.dispatches, fakeDispatch{workspaceID, conversationKey, text, occurrenceKey})
	return m.dispatch, m.dispatchErr
}

func (*fakeSDKConversationManager) Delete(context.Context, string, string) (int32, error) {
	return 1, nil
}
