package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"kandev-plugin-coordinator/server/governor"
)

const (
	ActionEnsure        = "coordinator.ensure"
	ActionStatus        = "coordinator.status"
	ActionReports       = "coordinator.reports"
	ActionRunCycle      = "coordinator.run-cycle"
	ActionRunStandup    = "coordinator.run-standup"
	ActionShadowObserve = "coordinator.shadow-observe"
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
	case ActionShadowObserve:
		if p.shadowObserver == nil {
			return actionJSON(map[string]any{"status": "unavailable", "reason": "shadow governor is opt-in and live board collection is unavailable; supply a normalized snapshot through a configured observer"})
		}
		var input governor.Observation
		decoder := json.NewDecoder(bytes.NewReader(req.Body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return nil, fmt.Errorf("coordinator: decoding shadow observation: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("coordinator: shadow observation must contain one JSON value")
		}
		// The Host-authenticated workspace is authoritative over the supplied
		// payload, preventing a caller from checkpointing another workspace.
		if input.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("coordinator: shadow observation workspace does not match verified context")
		}
		result, err := p.shadowObserver.Observe(ctx, input)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"status": "shadow", "result": result})
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
	return actionJSON(map[string]any{"status": status, "message": message, "config": config, "state": state})
}

func actionJSON(value any) (*pluginsdk.PluginActionResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.PluginActionResponse{Body: body, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
