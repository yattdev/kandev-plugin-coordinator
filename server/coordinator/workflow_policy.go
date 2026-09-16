package coordinator

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

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
	doc, err := p.loadDocument(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	checks := make([]PolicyCheck, 0, len(doc.WorkflowPolicy))
	for _, selected := range doc.WorkflowPolicy {
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
	doc, err := p.loadDocument(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return append([]WorkflowPolicy(nil), doc.WorkflowPolicy...), nil
}

func (p *Plugin) savePolicy(ctx context.Context, workspaceID string, policy []WorkflowPolicy) error {
	available, err := p.availablePolicySteps(ctx, workspaceID)
	if err != nil {
		return err
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
		if _, found := available[key]; !found {
			return fmt.Errorf("policy selection %q is not an available workflow step", key)
		}
		seen[key] = struct{}{}
		policy[index] = selected
	}
	_, err = p.updateDocument(ctx, workspaceID, func(doc *workspaceDocument) error {
		doc.WorkflowPolicy = append([]WorkflowPolicy(nil), policy...)
		return nil
	})
	return err
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
