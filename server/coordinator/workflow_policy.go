package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type WorkstepPolicy struct {
	WorkstepID string `json:"workstep_id"`
	Prompt     string `json:"prompt"`
}

type WorkflowPolicy struct {
	WorkflowID string           `json:"workflow_id"`
	Worksteps  []WorkstepPolicy `json:"worksteps"`
}

func workflowPolicyKey(workflowID string) string { return "workflow_policy:" + workflowID }

func (p *Plugin) loadWorkflowPolicy(ctx context.Context, workspaceID, workflowID string) (WorkflowPolicy, bool, error) {
	if workflowID == "" {
		return WorkflowPolicy{}, false, fmt.Errorf("workflow id is required")
	}
	value, found, err := p.Host().GetState(ctx, "workspace", workspaceID, workflowPolicyKey(workflowID))
	if err != nil || !found {
		return WorkflowPolicy{WorkflowID: workflowID}, false, err
	}
	policy := WorkflowPolicy{WorkflowID: workflowID}
	if err := mapInto(value, &policy); err != nil {
		return WorkflowPolicy{}, false, fmt.Errorf("decode workflow policy: %w", err)
	}
	return policy, true, nil
}

func (p *Plugin) saveWorkflowPolicy(ctx context.Context, workspaceID string, policy WorkflowPolicy) (WorkflowPolicy, error) {
	workflow, err := p.workspaceWorkflow(ctx, workspaceID, policy.WorkflowID)
	if err != nil {
		return WorkflowPolicy{}, err
	}
	steps, err := p.Host().Workflows().ListSteps(ctx, workflow.ID)
	if err != nil {
		return WorkflowPolicy{}, err
	}
	positions := make(map[string]int32, len(steps))
	for _, step := range steps {
		if step.WorkflowID == workflow.ID {
			positions[step.ID] = step.Position
		}
	}
	seen := make(map[string]struct{}, len(policy.Worksteps))
	for _, selected := range policy.Worksteps {
		if strings.TrimSpace(selected.WorkstepID) == "" {
			return WorkflowPolicy{}, fmt.Errorf("workstep id is required")
		}
		if _, duplicate := seen[selected.WorkstepID]; duplicate {
			return WorkflowPolicy{}, fmt.Errorf("duplicate workstep id %q", selected.WorkstepID)
		}
		seen[selected.WorkstepID] = struct{}{}
		if _, exists := positions[selected.WorkstepID]; !exists {
			return WorkflowPolicy{}, fmt.Errorf("workstep %q does not belong to workflow %q", selected.WorkstepID, workflow.ID)
		}
	}
	sort.SliceStable(policy.Worksteps, func(i, j int) bool {
		return positions[policy.Worksteps[i].WorkstepID] < positions[policy.Worksteps[j].WorkstepID]
	})
	value, err := structMap(policy)
	if err != nil {
		return WorkflowPolicy{}, err
	}
	if err := p.Host().SetState(ctx, "workspace", workspaceID, workflowPolicyKey(workflow.ID), value); err != nil {
		return WorkflowPolicy{}, err
	}
	return policy, nil
}

func (p *Plugin) workspaceWorkflow(ctx context.Context, workspaceID, workflowID string) (pluginsdk.Workflow, error) {
	if workspaceID == "" || workflowID == "" {
		return pluginsdk.Workflow{}, fmt.Errorf("workspace and workflow ids are required")
	}
	page := pluginsdk.Page{Limit: 100}
	for {
		items, info, err := p.Host().Workflows().List(ctx, workspaceID, page)
		if err != nil {
			return pluginsdk.Workflow{}, err
		}
		for _, workflow := range items {
			if workflow.ID == workflowID && workflow.WorkspaceID == workspaceID {
				return workflow, nil
			}
		}
		if info == nil || !info.HasMore {
			break
		}
		if info.NextCursor == "" {
			return pluginsdk.Workflow{}, fmt.Errorf("workflow pagination returned has_more without next cursor")
		}
		page.Cursor = info.NextCursor
	}
	return pluginsdk.Workflow{}, fmt.Errorf("workflow %q does not belong to verified workspace", workflowID)
}

func (p *Plugin) selectedChecks(ctx context.Context, workspaceID string) ([]PolicyCheck, error) {
	workflows, err := p.listAllWorkflows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var checks []PolicyCheck
	for _, workflow := range workflows {
		policy, found, err := p.loadWorkflowPolicy(ctx, workspaceID, workflow.ID)
		if err != nil {
			return nil, err
		}
		if !found || len(policy.Worksteps) == 0 {
			continue
		}
		steps, err := p.Host().Workflows().ListSteps(ctx, workflow.ID)
		if err != nil {
			return nil, err
		}
		byID := make(map[string]pluginsdk.WorkflowStep, len(steps))
		for _, step := range steps {
			if step.WorkflowID == workflow.ID {
				byID[step.ID] = step
			}
		}
		for _, selected := range policy.Worksteps {
			step, exists := byID[selected.WorkstepID]
			if !exists {
				continue
			}
			checks = append(checks, PolicyCheck{WorkflowID: workflow.ID, WorkflowName: workflow.Name, WorkstepID: step.ID, WorkstepName: step.Name, Prompt: selected.Prompt})
		}
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].WorkflowName != checks[j].WorkflowName {
			return checks[i].WorkflowName < checks[j].WorkflowName
		}
		return checks[i].WorkstepName < checks[j].WorkstepName
	})
	return checks, nil
}

func decodePolicyInput(body []byte) (struct {
	Operation  string           `json:"operation"`
	WorkflowID string           `json:"workflow_id"`
	Worksteps  []WorkstepPolicy `json:"worksteps"`
}, error) {
	var input struct {
		Operation  string           `json:"operation"`
		WorkflowID string           `json:"workflow_id"`
		Worksteps  []WorkstepPolicy `json:"worksteps"`
	}
	err := json.Unmarshal(body, &input)
	return input, err
}
