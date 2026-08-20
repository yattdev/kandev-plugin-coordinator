package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const ConversationKey = "coordinator"

var ErrConversationCapabilityUnavailable = errors.New("managed agent conversations are unavailable on this Kandev host")
var ErrConversationConfigurationRequired = errors.New("a usable coordinator agent profile is required")

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
	return r.Status == "sent" || r.Status == "queued" || r.Status == "started" || r.Status == "duplicate_occurrence"
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

type hostConversationManager struct {
	manager pluginsdk.AgentConversationManager
}

func newHostConversationManager(host pluginsdk.Host) ConversationManager {
	manager, ok := pluginsdk.AgentConversations(host)
	if !ok || manager == nil {
		return unavailableConversationManager{}
	}
	return hostConversationManager{manager: manager}
}

func (m hostConversationManager) Ensure(ctx context.Context, spec ConversationSpec) (ConversationDescriptor, error) {
	descriptor, status, err := m.manager.Ensure(ctx, pluginsdk.AgentConversationSpec{
		WorkspaceID: spec.WorkspaceID, ConversationKey: spec.Key,
		BasePrompt: spec.Instructions, AgentProfileID: spec.AgentProfileID,
	})
	if err != nil {
		return ConversationDescriptor{}, err
	}
	result := ConversationDescriptor{
		WorkspaceID: descriptor.WorkspaceID, Key: descriptor.ConversationKey,
		TaskID: descriptor.TaskID, SessionID: descriptor.SessionID, Status: status,
	}
	if status == "configuration_required" {
		return result, ErrConversationConfigurationRequired
	}
	return result, nil
}

func (m hostConversationManager) Dispatch(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	dispatch, err := m.manager.Dispatch(ctx, req.WorkspaceID, req.Key, req.Prompt, req.OccurrenceKey)
	if err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{
		Status: dispatch.Status, TaskID: dispatch.Descriptor.TaskID,
		SessionID: dispatch.SessionID, OccurrenceKey: req.OccurrenceKey,
	}, nil
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
