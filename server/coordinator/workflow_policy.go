package coordinator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"kandev-plugin-coordinator/server/durablestate"
)

const workflowPolicyRecordID = "coordinator-workflow-policy"

// selectedChecks resolves the plugin-owned policy against generic Host
// workflow readers. Deleted or moved selections stay saved but are unavailable
// until an operator changes them.
func (p *Plugin) selectedChecks(ctx context.Context, workspaceID string) ([]PolicyCheck, error) {
	workflows, err := p.listAllWorkflows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	byWorkflow := make(map[string]map[string]PolicyCheck, len(workflows))
	for _, workflow := range workflows {
		steps, err := p.Host().Workflows().ListSteps(ctx, workflow.ID)
		if err != nil {
			return nil, err
		}
		byWorkflow[workflow.ID] = make(map[string]PolicyCheck, len(steps))
		for _, step := range steps {
			if step.WorkflowID != workflow.ID {
				continue
			}
			byWorkflow[workflow.ID][step.ID] = PolicyCheck{
				WorkflowID: workflow.ID, WorkflowName: workflow.Name,
				WorkstepID: step.ID, WorkstepName: step.Name,
			}
		}
	}
	policy, err := p.policy(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	checks := make([]PolicyCheck, 0, len(policy))
	for _, selected := range policy {
		check, found := byWorkflow[selected.WorkflowID][selected.WorkstepID]
		if !found {
			continue
		}
		check.Prompt = selected.Prompt
		checks = append(checks, check)
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].WorkflowName != checks[j].WorkflowName {
			return checks[i].WorkflowName < checks[j].WorkflowName
		}
		if checks[i].WorkstepName != checks[j].WorkstepName {
			return checks[i].WorkstepName < checks[j].WorkstepName
		}
		return checks[i].WorkstepID < checks[j].WorkstepID
	})
	return checks, nil
}

func (p *Plugin) policy(ctx context.Context, workspaceID string) ([]WorkflowPolicy, error) {
	store, err := p.durablePolicyStore(ctx)
	if err != nil {
		return nil, err
	}
	record, found, err := store.GetRecord(ctx, workspaceID, workflowPolicyRecordID)
	if err != nil || !found {
		return nil, err
	}
	return policyFromBody(record.Body)
}

func (p *Plugin) savePolicy(ctx context.Context, workspaceID string, policy []WorkflowPolicy) error {
	available, err := p.availablePolicySteps(ctx, workspaceID)
	if err != nil {
		return err
	}
	previous, err := p.policy(ctx, workspaceID)
	if err != nil {
		return err
	}
	previousByKey := make(map[string]WorkflowPolicy, len(previous))
	for _, selected := range previous {
		previousByKey[selected.WorkflowID+"/"+selected.WorkstepID] = selected
	}
	seen := make(map[string]struct{}, len(policy))
	for index, selected := range policy {
		selected.WorkflowID = strings.TrimSpace(selected.WorkflowID)
		selected.WorkstepID = strings.TrimSpace(selected.WorkstepID)
		if selected.WorkflowID == "" || selected.WorkstepID == "" {
			return fmt.Errorf("policy selection %d requires workflow_id and workstep_id", index)
		}
		key := selected.WorkflowID + "/" + selected.WorkstepID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("policy selection %q is duplicated", key)
		}
		if _, found := available[key]; !found && previousByKey[key] == (WorkflowPolicy{}) {
			return fmt.Errorf("policy selection %q is not an available workflow step", key)
		}
		seen[key] = struct{}{}
		policy[index] = selected
	}
	store, err := p.durablePolicyStore(ctx)
	if err != nil {
		return err
	}
	token, err := store.AcquireLease(ctx, workspaceID, "coordinator-policy")
	if err != nil {
		return err
	}
	body := policyBody(policy)
	_, found, err := store.GetRecord(ctx, workspaceID, workflowPolicyRecordID)
	if err != nil {
		return err
	}
	if !found {
		_, err = store.AppendAdd(ctx, workspaceID, token, workflowPolicyRecordID, durablestate.KindFollowUp, body, durablestate.StorageInline)
	} else {
		_, err = store.AppendUpdate(ctx, workspaceID, token, workflowPolicyRecordID, body, durablestate.StorageInline)
	}
	return err
}

func policyBody(policy []WorkflowPolicy) map[string]any {
	items := make([]any, 0, len(policy))
	for _, selected := range policy {
		items = append(items, map[string]any{"workflow_id": selected.WorkflowID, "workstep_id": selected.WorkstepID, "prompt": selected.Prompt})
	}
	return map[string]any{"selections": items}
}

func policyFromBody(body map[string]any) ([]WorkflowPolicy, error) {
	raw, found := body["selections"]
	if !found {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("durable workflow policy has invalid selections")
	}
	policy := make([]WorkflowPolicy, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("durable workflow policy has invalid selection")
		}
		workflowID, workflowOK := item["workflow_id"].(string)
		workstepID, workstepOK := item["workstep_id"].(string)
		prompt, _ := item["prompt"].(string)
		if !workflowOK || !workstepOK {
			return nil, fmt.Errorf("durable workflow policy selection requires IDs")
		}
		policy = append(policy, WorkflowPolicy{WorkflowID: workflowID, WorkstepID: workstepID, Prompt: prompt})
	}
	return policy, nil
}

func (p *Plugin) availablePolicySteps(ctx context.Context, workspaceID string) (map[string]struct{}, error) {
	workflows, err := p.listAllWorkflows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	available := map[string]struct{}{}
	for _, workflow := range workflows {
		steps, err := p.Host().Workflows().ListSteps(ctx, workflow.ID)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			if step.WorkflowID == workflow.ID {
				available[workflow.ID+"/"+step.ID] = struct{}{}
			}
		}
	}
	return available, nil
}
