package coordinator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManifestDeclaresManagedConversationContract(t *testing.T) {
	manifest := readRepositoryFile(t, "manifest.yaml")
	require.Contains(t, manifest, "agent_conversation: true")
	require.NotContains(t, manifest, "api_write: [tasks, messages]")
	for _, action := range []string{
		"coordinator.ensure", "coordinator.status", "coordinator.reports",
		"coordinator.run-cycle", "coordinator.run-standup", "coordinator.workflow-policy",
	} {
		require.Contains(t, manifest, action)
	}
	require.Contains(t, manifest, "name: get_coordinator_state")
	require.Contains(t, manifest, "name: publish_report")
}

func TestPromptAssetsAreAdaptedAndSelfContained(t *testing.T) {
	base := readRepositoryFile(t, filepath.Join("prompts", "coordinator.md"))
	cycle := readRepositoryFile(t, filepath.Join("prompts", "monitoring-cycle.md"))
	report := readRepositoryFile(t, filepath.Join("prompts", "default-report-template.md"))
	combined := base + cycle + report

	require.Contains(t, base, "2026-08-16.2")
	for _, required := range []string{
		"get_coordinator_state", "publish_report", "critical", "degrad",
		"healthy", "stalled", "blocked", "anomaly", "NEEDS YOUR DECISION",
	} {
		require.Contains(t, strings.ToLower(combined), strings.ToLower(required))
	}
	require.NotContains(t, combined, "/data/home/Code/coordinator")
	require.NotContains(t, strings.ToLower(base), "host crontab")
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(contents)
}
