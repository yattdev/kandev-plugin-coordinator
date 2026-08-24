package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	ActionEnsure         = "coordinator.ensure"
	ActionStatus         = "coordinator.status"
	ActionReports        = "coordinator.reports"
	ActionAutomationBind = "coordinator.automation-bind"
	ActionAutomations    = "coordinator.automations"
)

func (p *Plugin) HandleAction(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if req == nil || req.Context.WorkspaceID == "" {
		return nil, fmt.Errorf("coordinator: verified workspace context is required")
	}
	workspaceID := req.Context.WorkspaceID
	switch req.ActionKey {
	case ActionEnsure:
		config, err := p.config(ctx)
		if err != nil {
			return nil, err
		}
		descriptor, err := ensureConversation(ctx, p.manager, workspaceID, config)
		if errors.Is(err, ErrConversationCapabilityUnavailable) {
			return actionJSON(map[string]any{"status": "unavailable", "error": err.Error()})
		}
		if errors.Is(err, ErrConversationConfigurationRequired) {
			return actionJSON(map[string]any{"status": "configuration_required", "error": err.Error()})
		}
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"status": "ready", "conversation": descriptor})
	case ActionStatus:
		return p.handleStatusAction(ctx, workspaceID)
	case ActionReports:
		var input struct {
			Cursor string `json:"cursor"`
			Limit  int    `json:"limit"`
		}
		if len(req.Body) != 0 {
			if err := json.Unmarshal(req.Body, &input); err != nil {
				return nil, fmt.Errorf("coordinator: decoding reports request: %w", err)
			}
		}
		page, err := p.listReports(ctx, workspaceID, input.Cursor, input.Limit)
		if err != nil {
			return nil, err
		}
		return actionJSON(page)
	case ActionAutomationBind:
		var input struct {
			AutomationIDs []string `json:"automation_ids"`
		}
		if err := json.Unmarshal(req.Body, &input); err != nil {
			return nil, fmt.Errorf("coordinator: decoding automation binding request: %w", err)
		}
		bindings, err := p.bindAutomations(ctx, workspaceID, input.AutomationIDs)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"bindings": bindings})
	case ActionAutomations:
		items, err := p.listAllAutomations(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"automations": items})
	default:
		return nil, fmt.Errorf("coordinator: unknown action %q", req.ActionKey)
	}
}

func (p *Plugin) handleStatusAction(ctx context.Context, workspaceID string) (*pluginsdk.PluginActionResponse, error) {
	config, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	state, err := p.readState(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	status := "ready"
	message := ""
	if err := config.ReadyForRun(); err != nil {
		status, message = "configuration_required", err.Error()
	} else if _, err := ensureConversation(ctx, p.manager, workspaceID, config); errors.Is(err, ErrConversationCapabilityUnavailable) {
		status, message = "unavailable", err.Error()
	} else if errors.Is(err, ErrConversationConfigurationRequired) {
		status, message = "configuration_required", err.Error()
	} else if err != nil {
		status, message = "error", err.Error()
	}
	return actionJSON(map[string]any{"status": status, "message": message, "config": config, "state": state, "capabilities": state.Capabilities})
}

func actionJSON(value any) (*pluginsdk.PluginActionResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.PluginActionResponse{Body: body, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
