package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type Plugin struct {
	pluginsdk.UnimplementedPlugin

	manager         ConversationManager
	managerInjected bool
	nowFn           func() time.Time
	tickInterval    time.Duration
	locks           workspaceLocks
	runnerMu        sync.Mutex
	runnerCancel    context.CancelFunc
	runnerDone      chan struct{}
}

var (
	_ pluginsdk.Plugin          = (*Plugin)(nil)
	_ pluginsdk.ActionHandler   = (*Plugin)(nil)
	_ pluginsdk.AgentToolPlugin = (*Plugin)(nil)
)

func New() *Plugin { return &Plugin{nowFn: time.Now, tickInterval: time.Minute} }

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
	p.startRunner()
}

func (p *Plugin) startRunner() {
	p.runnerMu.Lock()
	defer p.runnerMu.Unlock()
	if p.runnerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.runnerCancel = cancel
	done := make(chan struct{})
	p.runnerDone = done
	interval := p.tickInterval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		defer close(done)
		_ = p.RunDue(ctx, p.now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = p.RunDue(ctx, p.now())
			}
		}
	}()
}

func (p *Plugin) Close() {
	p.runnerMu.Lock()
	cancel, done := p.runnerCancel, p.runnerDone
	p.runnerCancel, p.runnerDone = nil, nil
	p.runnerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
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
