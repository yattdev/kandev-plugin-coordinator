package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	ActionEnsure         = "coordinator.ensure"
	ActionStatus         = "coordinator.status"
	ActionReports        = "coordinator.reports"
	ActionRunCycle       = "coordinator.run-cycle"
	ActionRunStandup     = "coordinator.run-standup"
	ActionWorkflowPolicy = "coordinator.workflow-policy"
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
		if err != nil {
			return actionJSON(map[string]any{"status": "configuration_required", "error": err.Error()})
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
	case ActionRunCycle, ActionRunStandup:
		var input struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := json.Unmarshal(req.Body, &input); err != nil {
			return nil, fmt.Errorf("coordinator: decoding manual run: %w", err)
		}
		trigger := TriggerCycle
		if req.ActionKey == ActionRunStandup {
			trigger = TriggerStandup
		}
		result, err := p.RunManual(ctx, workspaceID, trigger, input.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"dispatch": result})
	case ActionWorkflowPolicy:
		return p.handleWorkflowPolicyAction(ctx, workspaceID, req.Body)
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
	} else if err != nil {
		status, message = "error", err.Error()
	}
	return actionJSON(map[string]any{"status": status, "message": message, "config": config, "state": state})
}

func (p *Plugin) handleWorkflowPolicyAction(ctx context.Context, workspaceID string, body []byte) (*pluginsdk.PluginActionResponse, error) {
	input, err := decodePolicyInput(body)
	if err != nil {
		return nil, fmt.Errorf("coordinator: decoding workflow policy: %w", err)
	}
	if input.Operation == "get" {
		policy, found, err := p.loadWorkflowPolicy(ctx, workspaceID, input.WorkflowID)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"configured": found, "policy": policy})
	}
	if input.Operation != "save" {
		return nil, fmt.Errorf("coordinator: workflow policy operation must be get or save")
	}
	policy, err := p.saveWorkflowPolicy(ctx, workspaceID, WorkflowPolicy{WorkflowID: input.WorkflowID, Worksteps: input.Worksteps})
	if err != nil {
		return nil, err
	}
	return actionJSON(map[string]any{"configured": true, "policy": policy})
}

func actionJSON(value any) (*pluginsdk.PluginActionResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.PluginActionResponse{Body: body, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
