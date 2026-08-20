package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
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
