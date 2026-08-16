package main

import (
	"context"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// WorkflowPolicy is stored per verified workspace and workflow. A workflow is
// disabled when no worksteps are selected; an empty prompt intentionally adds
// no instruction before the default coordinator playbook.
type WorkflowPolicy struct {
	WorkflowID string `json:"workflow_id"`
	WorkstepIDs []string `json:"workstep_ids"`
	Prompt string `json:"prompt"`
}

func workflowPolicyKey(workflowID string) string { return "workflow_policy:" + workflowID }

func (p *coordinatorPlugin) workflowPolicy(ctx context.Context, workspaceID, workflowID string) (WorkflowPolicy, bool, error) {
	if workflowID == "" { return WorkflowPolicy{}, false, fmt.Errorf("workflow id is required") }
	value, found, err := p.Host().GetState(ctx, "workspace", workspaceID, workflowPolicyKey(workflowID))
	if err != nil || !found { return WorkflowPolicy{WorkflowID: workflowID}, false, err }
	policy := WorkflowPolicy{WorkflowID: workflowID}
	policy.Prompt, _ = value["prompt"].(string)
	if rawIDs, ok := value["workstep_ids"].([]any); ok { for _, raw := range rawIDs { if id, ok := raw.(string); ok { policy.WorkstepIDs = append(policy.WorkstepIDs, id) } } }
	return policy, true, nil
}

func (p *coordinatorPlugin) saveWorkflowPolicy(ctx context.Context, workspaceID string, policy WorkflowPolicy) error {
	if policy.WorkflowID == "" { return fmt.Errorf("workflow id is required") }
	return p.Host().SetState(ctx, "workspace", workspaceID, workflowPolicyKey(policy.WorkflowID), map[string]any{"prompt": policy.Prompt, "workstep_ids": policy.WorkstepIDs})
}
