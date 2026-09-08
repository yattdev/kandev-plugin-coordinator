package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestHostConversationManagerMapsCurrentSDKContract(t *testing.T) {
	sdkManager := &fakeSDKConversationManager{
		ensure: pluginsdk.AgentConversationDescriptor{
			TaskID: "task-1", SessionID: "session-1", WorkspaceID: "workspace-1",
			ConversationKey: ConversationKey,
		},
		ensureState: "created",
		dispatch: pluginsdk.AgentConversationDispatch{
			Status: "started", SessionID: "session-1",
			Descriptor: pluginsdk.AgentConversationDescriptor{TaskID: "task-1"},
		},
	}
	host := newFakeHost()
	host.conversations = sdkManager
	manager := newHostConversationManager(host)

	descriptor, err := manager.Ensure(context.Background(), ConversationSpec{
		WorkspaceID: "workspace-1", Key: ConversationKey, Instructions: "base",
	})
	require.NoError(t, err)
	require.Equal(t, "created", descriptor.Status)
	require.Equal(t, "session-1", descriptor.SessionID)
	require.Equal(t, "", sdkManager.ensureSpecs[0].AgentProfileID, "blank override must use the workspace default")

	result, err := manager.Dispatch(context.Background(), DispatchRequest{
		WorkspaceID: "workspace-1", Key: ConversationKey, Prompt: "wake",
		OccurrenceKey: "scheduled/workspace-1/cycle/2026-08-20/1",
	})
	require.NoError(t, err)
	require.True(t, result.Successful())
	require.Equal(t, "task-1", result.TaskID)
	require.Equal(t, "scheduled/workspace-1/cycle/2026-08-20/1", sdkManager.dispatches[0].occurrenceKey)
}

func TestEnsureMapsConfigurationRequiredWithoutPartialDescriptor(t *testing.T) {
	sdkManager := &fakeSDKConversationManager{ensureState: "configuration_required"}
	host := newFakeHost()
	host.conversations = sdkManager
	_, err := ensureConversation(context.Background(), newHostConversationManager(host), "workspace-1", mustConfig(t))
	require.ErrorIs(t, err, ErrConversationConfigurationRequired)
	require.Empty(t, sdkManager.ensure.TaskID)
}

func TestUnavailableConversationCapabilityFailsExplicitly(t *testing.T) {
	var host pluginsdk.Host = hostWithoutConversation{Host: newFakeHost()}
	_, err := ensureConversation(context.Background(), newHostConversationManager(host), "workspace-1", mustConfig(t))
	require.True(t, errors.Is(err, ErrConversationCapabilityUnavailable))
}

type hostWithoutConversation struct{ pluginsdk.Host }

func mustConfig(t *testing.T) Config {
	t.Helper()
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	return config
}
