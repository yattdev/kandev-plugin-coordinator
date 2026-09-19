package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
	"kandev-plugin-coordinator/server/governor"
)

func TestEnsureActionReturnsManagedDescriptor(t *testing.T) {
	manager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{
			TaskID: "hidden-task", SessionID: "session-1", WorkspaceID: "workspace-verified", ConversationKey: ConversationKey,
		},
		ensureState: "exists",
	}
	host := newFakeHost()
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: ActionEnsure,
		Context:   pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-verified"},
		Body:      []byte(`{"workspace_id":"workspace-untrusted"}`),
	})
	require.NoError(t, err)
	var body struct {
		Status       string                 `json:"status"`
		Conversation ConversationDescriptor `json:"conversation"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &body))
	require.Equal(t, "ready", body.Status)
	require.Equal(t, "workspace-verified", body.Conversation.WorkspaceID)
	require.Equal(t, ConversationKey, body.Conversation.Key)
	require.Equal(t, "workspace-verified", manager.ensureSpecs[0].WorkspaceID)
}

type fakeShadowObserver struct{ input governor.Observation }

func (f *fakeShadowObserver) Observe(_ context.Context, input governor.Observation) (governor.Result, error) {
	f.input = input
	return governor.Result{Decision: governor.TierNone}, nil
}

func TestShadowObservationActionIsOptInAndWorkspaceBound(t *testing.T) {
	host := newFakeHost()
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionShadowObserve, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}})
	require.NoError(t, err)
	require.Contains(t, string(response.Body), "unavailable")
	observer := &fakeShadowObserver{}
	plugin.SetShadowObserver(observer)
	body := []byte(`{"schema_version":"shadow-governor/v1","workspace_id":"workspace-1","observed_at":"2026-09-19T12:00:00Z","event_id":"event-1","provenance":"fixture","complete":true,"tasks":[]}`)
	_, err = plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionShadowObserve, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"}, Body: body})
	require.NoError(t, err)
	require.Equal(t, "event-1", observer.input.EventID)
	require.Equal(t, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), observer.input.ObservedAt)
}

func TestEnsureActionReturnsTypedConfigurationState(t *testing.T) {
	manager := &fakeSDKConversationManager{ensureState: "configuration_required"}
	host := newFakeHost()
	plugin := NewWithConversationManager(hostConversationManager{manager: manager})
	plugin.UnimplementedPlugin.SetHost(host)
	response, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: ActionEnsure, Context: pluginsdk.VerifiedActionContext{WorkspaceID: "workspace-1"},
	})
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body, &body))
	require.Equal(t, "configuration_required", body["status"])
	require.Empty(t, manager.ensure.TaskID)
}

func TestActionsRejectMissingVerifiedWorkspace(t *testing.T) {
	plugin := New()
	_, err := plugin.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{ActionKey: ActionStatus})
	require.ErrorContains(t, err, "verified workspace context")
}
