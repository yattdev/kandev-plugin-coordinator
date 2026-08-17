package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const ConversationKey = "coordinator"

var ErrConversationCapabilityUnavailable = errors.New("managed agent conversations are unavailable on this Kandev host")

type ConversationSpec struct {
	WorkspaceID        string
	Key                string
	AgentProfileID     string
	Instructions       string
	InstructionVersion string
}

type ConversationDescriptor struct {
	WorkspaceID string `json:"workspace_id"`
	Key         string `json:"key"`
	TaskID      string `json:"task_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Status      string `json:"status"`
}

type DispatchRequest struct {
	WorkspaceID   string
	Key           string
	OccurrenceKey string
	Prompt        string
}

type DispatchResult struct {
	Status        string `json:"status"`
	TaskID        string `json:"task_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	OccurrenceKey string `json:"occurrence_key"`
}

func (r DispatchResult) Successful() bool {
	return r.Status == "sent" || r.Status == "queued" || r.Status == "started" || r.Status == "duplicate"
}

type ConversationManager interface {
	Ensure(context.Context, ConversationSpec) (ConversationDescriptor, error)
	Dispatch(context.Context, DispatchRequest) (DispatchResult, error)
}

type unavailableConversationManager struct{}

func (unavailableConversationManager) Ensure(context.Context, ConversationSpec) (ConversationDescriptor, error) {
	return ConversationDescriptor{}, ErrConversationCapabilityUnavailable
}

func (unavailableConversationManager) Dispatch(context.Context, DispatchRequest) (DispatchResult, error) {
	return DispatchResult{}, ErrConversationCapabilityUnavailable
}

// newHostConversationManager is the only backend binding point for the parent
// Host.AgentConversations contract. The current released SDK has no such
// optional Host extension, so old hosts fail explicitly instead of creating a
// visible fallback task. Replace this function when the parent contract lands.
func newHostConversationManager(pluginsdk.Host) ConversationManager {
	return unavailableConversationManager{}
}

func ensureConversation(ctx context.Context, manager ConversationManager, workspaceID string, config Config) (ConversationDescriptor, error) {
	if manager == nil {
		return ConversationDescriptor{}, ErrConversationCapabilityUnavailable
	}
	if err := config.ReadyForRun(); err != nil {
		return ConversationDescriptor{}, fmt.Errorf("coordinator configuration required: %w", err)
	}
	return manager.Ensure(ctx, ConversationSpec{
		WorkspaceID: workspaceID, Key: ConversationKey, AgentProfileID: config.AgentProfile,
		Instructions: config.BasePrompt, InstructionVersion: "2026-08-16.2",
	})
}
