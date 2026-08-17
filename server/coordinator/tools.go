package coordinator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	ToolGetState      = "get_coordinator_state"
	ToolPublishReport = "publish_report"
)

func (p *Plugin) InvokeAgentTool(ctx context.Context, req *pluginsdk.AgentToolRequest) (*pluginsdk.AgentToolResult, error) {
	if req == nil || req.Context.WorkspaceID == "" {
		return toolError("A verified workspace context is required."), nil
	}
	workspaceID := req.Context.WorkspaceID
	switch req.Name {
	case ToolGetState:
		state, err := p.readState(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		return toolJSON(map[string]any{"state": state})
	case ToolPublishReport:
		var input PublishReportInput
		if err := mapInto(req.Arguments, &input); err != nil {
			return toolError(fmt.Sprintf("Invalid report payload: %v", err)), nil
		}
		artifact, state, err := p.publishReport(ctx, workspaceID, input)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolJSON(map[string]any{"artifact": artifact, "state": state})
	default:
		return toolError("Unknown coordinator tool."), nil
	}
}

func toolJSON(value map[string]any) (*pluginsdk.AgentToolResult, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.AgentToolResult{Text: string(body), StructuredContent: value}, nil
}

func toolError(message string) *pluginsdk.AgentToolResult {
	return &pluginsdk.AgentToolResult{Text: message, IsError: true}
}
