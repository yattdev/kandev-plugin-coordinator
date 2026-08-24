package coordinator

import (
	"context"
	"fmt"
	"strings"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// principalStatus reads the opaque, host-issued Workspace Coordinator
// principal. It never stores or derives backing task/session identifiers.
func (p *Plugin) principalStatus(ctx context.Context, workspaceID string) (CapabilityState, *pluginsdk.WorkspaceAgentPrincipalStatus, error) {
	reader := p.Host().WorkspaceAgentPrincipals()
	principal, err := reader.Get(ctx, workspaceID, ConversationKey)
	if err != nil {
		return CapabilityState{Status: "degraded", Reason: "Coordinator principal status could not be read."}, nil, err
	}
	if principal == nil {
		return CapabilityState{Status: "unavailable", Reason: "Operator consent is required for this Workspace Coordinator."}, nil, nil
	}
	status, err := reader.Status(ctx, workspaceID, ConversationKey)
	if err != nil {
		return CapabilityState{Status: "degraded", Reason: "Coordinator grant status could not be read."}, nil, err
	}
	if status == nil || status.State != "active" {
		return CapabilityState{Status: "unavailable", Reason: "Coordinator authority is revoked or inactive."}, status, nil
	}
	for _, capability := range status.GrantedCapabilities {
		if strings.EqualFold(capability, "orchestrate") {
			return CapabilityState{Status: "available"}, status, nil
		}
	}
	return CapabilityState{Status: "unavailable", Reason: "Operator consent does not include Workspace + Assist."}, status, nil
}

func (p *Plugin) principalAudit(ctx context.Context, workspaceID string) ([]pluginsdk.WorkspaceAgentPrincipalAuditEvent, error) {
	items, info, err := p.Host().WorkspaceAgentPrincipals().ListAudit(ctx, workspaceID, ConversationKey, pluginsdk.Page{Limit: 100})
	if err != nil {
		return nil, err
	}
	if info != nil && info.HasMore {
		return nil, fmt.Errorf("coordinator principal audit exceeds bounded page")
	}
	return items, nil
}
