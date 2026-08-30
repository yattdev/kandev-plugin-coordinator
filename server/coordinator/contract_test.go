package coordinator

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	require.True(t, config.MonitoringEnabled)
	require.Equal(t, 45, config.CycleIntervalMinutes)
	require.Equal(t, "weekdays", config.ScheduleDays)
	require.Equal(t, "08:00", config.WindowStart)
	require.Equal(t, "18:00", config.WindowEnd)
	require.Equal(t, "07:55", config.DailyReportTime)
	require.Equal(t, "America/Montreal", config.Timezone)
	require.NotEmpty(t, config.BasePrompt)
	require.NotEmpty(t, config.ReportTemplate)
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	tests := []map[string]any{
		{"cycle_interval_minutes": float64(4)},
		{"cycle_interval_minutes": float64(1441)},
		{"timezone": "Mars/Olympus"},
		{"daily_report_time": "8am"},
		{"schedule_days": "sometimes"},
		{"monitoring_window_start": "08:00", "monitoring_window_end": "08:00"},
	}
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

func TestOccurrenceKeysAreStable(t *testing.T) {
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	now := time.Date(2026, 11, 2, 13, 25, 0, 0, time.UTC)
	first, ok, err := CycleOccurrenceKey("workspace-1", now, config)
	require.NoError(t, err)
	require.True(t, ok)
	second, ok, err := CycleOccurrenceKey("workspace-1", now.Add(time.Minute), config)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, second)
}

func TestBlankProfileUsesWorkspaceDefault(t *testing.T) {
	config, err := ConfigFrom(nil)
	require.NoError(t, err)
	require.NoError(t, config.ReadyForRun())
}
