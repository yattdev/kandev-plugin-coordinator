package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const (
	statusAction        = "coordinator.status"
	reportAction        = "coordinator.report"
	policyAction        = "coordinator.workflow-policy"
	statusTool          = "coordinator_status"
	reportTool          = "coordinator_record_report"
	coordinatorStateKey = "coordinator_state"
)

type CoordinatorState struct {
	LastReportAt         string `json:"last_report_at,omitempty"`
	LastReportDispatchAt string `json:"last_report_dispatch_at,omitempty"`
	LastCycleAt          string `json:"last_cycle_at,omitempty"`
	Report               string `json:"report,omitempty"`
}
type coordinatorPlugin struct {
	pluginsdk.UnimplementedPlugin
	schedulerOnce sync.Once
}

var (
	_ pluginsdk.Plugin          = (*coordinatorPlugin)(nil)
	_ pluginsdk.ActionHandler   = (*coordinatorPlugin)(nil)
	_ pluginsdk.AgentToolPlugin = (*coordinatorPlugin)(nil)
)

func (p *coordinatorPlugin) SetHost(host pluginsdk.Host) {
	p.UnimplementedPlugin.SetHost(host)
	p.schedulerOnce.Do(func() { go p.schedulerLoop() })
}
func (p *coordinatorPlugin) schedulerLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		_ = p.dispatchDue(context.Background(), time.Now())
	}
}
func (p *coordinatorPlugin) state(ctx context.Context, workspaceID string) (CoordinatorState, error) {
	value, found, err := p.Host().GetState(ctx, "workspace", workspaceID, coordinatorStateKey)
	if err != nil || !found {
		return CoordinatorState{}, err
	}
	lastReportAt, _ := value["last_report_at"].(string)
	lastReportDispatchAt, _ := value["last_report_dispatch_at"].(string)
	lastCycleAt, _ := value["last_cycle_at"].(string)
	report, _ := value["report"].(string)
	return CoordinatorState{LastReportAt: lastReportAt, LastReportDispatchAt: lastReportDispatchAt, LastCycleAt: lastCycleAt, Report: report}, nil
}
func (p *coordinatorPlugin) saveState(ctx context.Context, workspaceID string, state CoordinatorState) error {
	return p.Host().SetState(ctx, "workspace", workspaceID, coordinatorStateKey, map[string]any{"last_report_at": state.LastReportAt, "last_report_dispatch_at": state.LastReportDispatchAt, "last_cycle_at": state.LastCycleAt, "report": state.Report})
}
func (p *coordinatorPlugin) saveReport(ctx context.Context, workspaceID, report string) (CoordinatorState, error) {
	if report == "" {
		return CoordinatorState{}, fmt.Errorf("report is required")
	}
	state, err := p.state(ctx, workspaceID)
	if err != nil {
		return CoordinatorState{}, err
	}
	state.LastReportAt, state.Report = time.Now().UTC().Format(time.RFC3339), report
	err = p.saveState(ctx, workspaceID, state)
	return state, err
}
func (p *coordinatorPlugin) status(ctx context.Context, workspaceID string) (map[string]any, error) {
	if p.Host() == nil {
		return nil, fmt.Errorf("coordinator: host unavailable")
	}
	values, err := p.Host().GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	config, err := configFrom(values)
	if err != nil {
		return nil, err
	}
	state, err := p.state(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	next, err := nextStandup(time.Now(), config)
	if err != nil {
		return nil, err
	}
	return map[string]any{"config": config, "state": state, "next_standup_at": next.UTC().Format(time.RFC3339)}, nil
}
func (p *coordinatorPlugin) HandleAction(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if req == nil || req.Context.WorkspaceID == "" {
		return nil, fmt.Errorf("coordinator: verified workspace context is required")
	}
	switch req.ActionKey {
	case statusAction:
		status, err := p.status(ctx, req.Context.WorkspaceID)
		if err != nil {
			return nil, err
		}
		return actionJSON(status)
	case reportAction:
		report, err := reqBodyReport(req.Body)
		if err != nil {
			return nil, err
		}
		state, err := p.saveReport(ctx, req.Context.WorkspaceID, report)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"state": state})
	case policyAction:
		return p.handleWorkflowPolicy(ctx, req.Context.WorkspaceID, req.Body)
	default:
		return nil, fmt.Errorf("coordinator: unknown action %q", req.ActionKey)
	}
}
func reqBodyReport(body []byte) (string, error) {
	var input struct {
		Report string `json:"report"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return "", fmt.Errorf("coordinator: decoding report: %w", err)
	}
	return input.Report, nil
}
func actionJSON(value any) (*pluginsdk.PluginActionResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.PluginActionResponse{Body: body, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
func (p *coordinatorPlugin) InvokeAgentTool(ctx context.Context, req *pluginsdk.AgentToolRequest) (*pluginsdk.AgentToolResult, error) {
	if req == nil || req.Context.WorkspaceID == "" {
		return &pluginsdk.AgentToolResult{Text: "A verified workspace context is required.", IsError: true}, nil
	}
	if req.Name == statusTool {
		status, err := p.status(ctx, req.Context.WorkspaceID)
		if err != nil {
			return nil, err
		}
		return toolJSON(status)
	}
	if req.Name == reportTool {
		report, _ := req.Arguments["report"].(string)
		state, err := p.saveReport(ctx, req.Context.WorkspaceID, report)
		if err != nil {
			return &pluginsdk.AgentToolResult{Text: err.Error(), IsError: true}, nil
		}
		return toolJSON(map[string]any{"state": state})
	}
	return &pluginsdk.AgentToolResult{Text: "Unknown coordinator tool.", IsError: true}, nil
}
func toolJSON(value map[string]any) (*pluginsdk.AgentToolResult, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.AgentToolResult{Text: string(body), StructuredContent: value}, nil
}

type workflowPolicyInput struct {
	Operation  string           `json:"operation"`
	WorkflowID string           `json:"workflow_id"`
	Worksteps  []WorkstepPolicy `json:"worksteps"`
}

func (p *coordinatorPlugin) handleWorkflowPolicy(ctx context.Context, workspaceID string, body []byte) (*pluginsdk.PluginActionResponse, error) {
	var input workflowPolicyInput
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("coordinator: decoding workflow policy: %w", err)
	}
	if input.Operation == "get" {
		policy, found, err := p.workflowPolicy(ctx, workspaceID, input.WorkflowID)
		if err != nil {
			return nil, err
		}
		return actionJSON(map[string]any{"policy": policy, "configured": found})
	}
	if input.Operation != "save" {
		return nil, fmt.Errorf("coordinator: workflow policy operation must be get or save")
	}
	policy := WorkflowPolicy{WorkflowID: input.WorkflowID, Worksteps: input.Worksteps}
	if err := p.saveWorkflowPolicy(ctx, workspaceID, policy); err != nil {
		return nil, err
	}
	return actionJSON(map[string]any{"policy": policy})
}
