package coordinator

import (
	"context"
	"sort"
)

// selectedChecks reads the workflow-step policy owned by Kandev. The plugin
// deliberately stores no second copy, so workflow settings remain the sole
// source of truth for both scheduled and manual runs.
func (p *Plugin) selectedChecks(ctx context.Context, workspaceID string) ([]PolicyCheck, error) {
	workflows, err := p.listAllWorkflows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var checks []PolicyCheck
	for _, workflow := range workflows {
		steps, err := p.Host().Workflows().ListSteps(ctx, workflow.ID)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			if step.WorkflowID != workflow.ID || !step.CoordinatorMonitored {
				continue
			}
			checks = append(checks, PolicyCheck{
				WorkflowID: workflow.ID, WorkflowName: workflow.Name,
				WorkstepID: step.ID, WorkstepName: step.Name,
				Prompt: step.CoordinatorPrompt,
			})
		}
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
