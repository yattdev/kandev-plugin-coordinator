package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type Plugin struct {
	pluginsdk.UnimplementedPlugin

	manager         ConversationManager
	managerInjected bool
	nowFn           func() time.Time
	locks           workspaceLocks
}

var (
	_ pluginsdk.Plugin          = (*Plugin)(nil)
	_ pluginsdk.ActionHandler   = (*Plugin)(nil)
	_ pluginsdk.AgentToolPlugin = (*Plugin)(nil)
)

func New() *Plugin { return &Plugin{nowFn: time.Now} }

func NewWithConversationManager(manager ConversationManager) *Plugin {
	p := New()
	p.manager = manager
	p.managerInjected = true
	return p
}

func (p *Plugin) now() time.Time {
	if p.nowFn == nil {
		return time.Now()
	}
	return p.nowFn()
}

func (p *Plugin) SetHost(host pluginsdk.Host) {
	p.UnimplementedPlugin.SetHost(host)
	if !p.managerInjected {
		p.manager = newHostConversationManager(host)
	}
}

func (p *Plugin) config(ctx context.Context) (Config, error) {
	if p.Host() == nil {
		return Config{}, fmt.Errorf("coordinator: host unavailable")
	}
	values, err := p.Host().GetConfig(ctx)
	if err != nil {
		return Config{}, err
	}
	return ConfigFrom(values)
}

func (p *Plugin) listAllWorkspaces(ctx context.Context) ([]pluginsdk.Workspace, error) {
	var all []pluginsdk.Workspace
	page := pluginsdk.Page{Limit: 100}
	for {
		items, info, err := p.Host().Workspaces().List(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if info == nil || !info.HasMore {
			return all, nil
		}
		if info.NextCursor == "" {
			return nil, fmt.Errorf("workspace pagination returned has_more without next cursor")
		}
		page.Cursor = info.NextCursor
	}
}

func (p *Plugin) listAllWorkflows(ctx context.Context, workspaceID string) ([]pluginsdk.Workflow, error) {
	var all []pluginsdk.Workflow
	page := pluginsdk.Page{Limit: 100}
	for {
		items, info, err := p.Host().Workflows().List(ctx, workspaceID, page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if info == nil || !info.HasMore {
			return all, nil
		}
		if info.NextCursor == "" {
			return nil, fmt.Errorf("workflow pagination returned has_more without next cursor")
		}
		page.Cursor = info.NextCursor
	}
}
