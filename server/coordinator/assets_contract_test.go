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
	require.Contains(t, manifest, "task_relations")
	require.NotContains(t, manifest, "api_write: [tasks, messages]")
	require.NotContains(t, manifest, `min_kandev_version: "0.88.0"`)
	for _, action := range []string{
		"coordinator.ensure", "coordinator.status", "coordinator.reports",
	} {
		require.Contains(t, manifest, action)
	}
	require.NotContains(t, manifest, "coordinator.workflow-policy")
	require.Contains(t, manifest, "name: get_coordinator_state")
	require.Contains(t, manifest, "name: publish_report")
}

func TestUIBindsCurrentHostContract(t *testing.T) {
	page := readRepositoryFile(t, filepath.Join("ui", "src", "coordinator-page.ts"))
	client := readRepositoryFile(t, filepath.Join("ui", "src", "coordinator-client.ts"))
	require.Contains(t, page, "host.context.getActiveWorkspaceId()")
	require.Contains(t, page, "conversationKey: state.ensure.conversation.key")
	require.Contains(t, page, "sessionId: state.ensure.conversation.session_id")
	require.NotContains(t, page, "conversation: state.ensure.conversation")
	require.Contains(t, client, "workspaceId: this.workspaceId")
	require.NotContains(t, client, "idempotency_key")
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
		"[Coordinator flag]", "at most one task",
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
