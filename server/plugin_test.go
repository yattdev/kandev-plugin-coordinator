package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

type actionFakeHost struct {
	pluginsdk.UnimplementedHostData
	state map[string]map[string]any
	config map[string]any
}

func newActionFakeHost() *actionFakeHost { return &actionFakeHost{state: map[string]map[string]any{}} }
func actionStateKey(scope, id, key string) string { return scope + "/" + id + "/" + key }
func (h *actionFakeHost) GetState(_ context.Context, scope, id, key string) (map[string]any, bool, error) { value, found := h.state[actionStateKey(scope, id, key)]; return value, found, nil }
func (h *actionFakeHost) SetState(_ context.Context, scope, id, key string, value map[string]any) error { h.state[actionStateKey(scope, id, key)] = value; return nil }
func (*actionFakeHost) DeleteState(context.Context, string, string, string) error { return nil }
func (*actionFakeHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) { return nil, nil }
func (h *actionFakeHost) GetConfig(context.Context) (map[string]any, error) { return h.config, nil }
func (*actionFakeHost) RevealSecret(context.Context, string) (string, error) { return "", nil }
func (*actionFakeHost) GetSecret(context.Context, string) (string, bool, error) { return "", false, nil }
func (*actionFakeHost) SetSecret(context.Context, string, string) error { return nil }
func (*actionFakeHost) DeleteSecret(context.Context, string) error { return nil }
func (*actionFakeHost) EmitEvent(context.Context, string, map[string]any) error { return nil }

var _ pluginsdk.Host = (*actionFakeHost)(nil)

func TestReportActionStoresOnlyInVerifiedWorkspace(t *testing.T) {
	host := newActionFakeHost(); plugin := &coordinatorPlugin{}; plugin.SetHost(host)
	_, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: reportAction, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}, Body: []byte(`{"report":"All clear"}`)})
	require.NoError(t, err)
	value, found, err := host.GetState(context.Background(), "workspace", "workspace-1", coordinatorStateKey)
	require.NoError(t, err); require.True(t, found); require.Equal(t, "All clear", value["report"])
}

func TestWorkflowPolicyActionPersistsPerWorkstepPrompts(t *testing.T) {
	host := newActionFakeHost(); plugin := &coordinatorPlugin{}; plugin.SetHost(host)
	body := []byte(`{"operation":"save","workflow_id":"workflow-1","worksteps":[{"workstep_id":"work-1","prompt":"Check blockers"}]}`)
	_, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: policyAction, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}, Body: body})
	require.NoError(t, err)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: policyAction, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}, Body: []byte(`{"operation":"get","workflow_id":"workflow-1"}`)})
	require.NoError(t, err)
	var decoded map[string]any; require.NoError(t, json.Unmarshal(response.Body, &decoded)); require.Equal(t, true, decoded["configured"])
}
