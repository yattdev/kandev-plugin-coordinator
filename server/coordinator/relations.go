package coordinator

import (
	"context"
	"fmt"
	"strings"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// inspectTaskRelations is the narrow bridge to Kandev's provider-neutral
// relationship reader. It deliberately returns the SDK's compact projection:
// Coordinator policy must not persist or infer authority from relationships.
// The host verifies that taskID belongs to workspaceID and collapses foreign
// and unknown targets to NotFound.
func (p *Plugin) inspectTaskRelations(ctx context.Context, workspaceID, taskID string) (*pluginsdk.TaskRelations, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("coordinator: workspace and task identifiers are required for relation inspection")
	}
	if p.Host() == nil {
		return nil, fmt.Errorf("coordinator: host unavailable")
	}
	return p.Host().TaskRelations().Get(ctx, workspaceID, taskID)
}
