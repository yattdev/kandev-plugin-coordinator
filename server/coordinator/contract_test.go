package coordinator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	require.NotEmpty(t, config.BasePrompt)
	require.NotEmpty(t, config.ReportTemplate)
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	tests := []map[string]any{{"base_prompt": " "}, {"report_template": " "}}
	for _, values := range tests {
		_, err := ConfigFrom(values)
		require.Error(t, err, values)
	}
}

func TestPromptCompositionKeepsSafetyInvariantsLast(t *testing.T) {
	checks := []PolicyCheck{
		{WorkflowID: "wf-b", WorkflowName: "Beta", WorkstepID: "step-2", WorkstepName: "Review", Prompt: "review carefully"},
		{WorkflowID: "wf-a", WorkflowName: "Alpha", WorkstepID: "step-1", WorkstepName: "Work", Prompt: "check blockers"},
	}
	prompt := ComposeOccurrencePrompt("base", checks, TriggerCycle, "")
	require.Less(t, strings.Index(prompt, "Alpha / Work"), strings.Index(prompt, "Beta / Review"))
	require.True(t, strings.HasSuffix(prompt, strings.TrimSpace(SafetyInvariants)))
	require.Contains(t, prompt, "WAKE:CYCLE")
}

func TestBlankProfileUsesWorkspaceDefault(t *testing.T) {
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	require.NoError(t, config.ReadyForRun())
}
