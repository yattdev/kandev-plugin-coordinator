package coordinator

import (
	"context"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"github.com/stretchr/testify/require"
)

func TestPrincipalStatusUsesOpaqueLogicalCoordinatorIdentity(t *testing.T) {
	host := newFakeHost()
	host.principal = &pluginsdk.WorkspaceAgentPrincipal{ID: "opaque-principal", WorkspaceID: "workspace-1", LogicalKey: ConversationKey}
	host.principalStatus = &pluginsdk.WorkspaceAgentPrincipalStatus{PrincipalID: "opaque-principal", State: "active", GrantedCapabilities: []string{"orchestrate"}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	state, status, err := plugin.principalStatus(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, "available", state.Status)
	require.Equal(t, "opaque-principal", status.PrincipalID)
}

func TestPrincipalStatusFailsClosedWithoutWorkspaceAssist(t *testing.T) {
	host := newFakeHost()
	host.principal = &pluginsdk.WorkspaceAgentPrincipal{ID: "opaque-principal", WorkspaceID: "workspace-1", LogicalKey: ConversationKey}
	host.principalStatus = &pluginsdk.WorkspaceAgentPrincipalStatus{PrincipalID: "opaque-principal", State: "active", GrantedCapabilities: []string{"inspect"}}
	plugin := New()
	plugin.UnimplementedPlugin.SetHost(host)
	state, _, err := plugin.principalStatus(context.Background(), "workspace-1")
	require.NoError(t, err)
	require.Equal(t, "unavailable", state.Status)
	require.Contains(t, state.Reason, "Workspace + Assist")
}
