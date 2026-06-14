package agent

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/core/session"
)

// Pool 管理多个对话会话，支持切换当前活跃会话。
type Pool struct {
	agent   *Agent
	sessions []*namedSession
	current  int
}

type namedSession struct {
	label   string
	session *session.Holder
}

// NewPool 创建一个空的会话池。
func (a *Agent) NewPool() *Pool {
	return &Pool{agent: a}
}

// Add 向池中添加一个新会话，返回其索引。
func (p *Pool) Add(label, prompt string) int {
	p.sessions = append(p.sessions, &namedSession{
		label:   label,
		session: p.agent.NewSession(prompt, 8),
	})
	return len(p.sessions) - 1
}

// Switch 切换到指定索引的会话。
func (p *Pool) Switch(idx int) error {
	if idx < 0 || idx >= len(p.sessions) {
		return fmt.Errorf("index %d out of range [0, %d)", idx, len(p.sessions))
	}
	p.current = idx
	return nil
}

// Current 返回当前活跃会话。
func (p *Pool) Current() *session.Holder {
	if len(p.sessions) == 0 {
		return nil
	}
	return p.sessions[p.current].session
}

// CurrentLabel 返回当前活跃会话的标签。
func (p *Pool) CurrentLabel() string {
	if len(p.sessions) == 0 {
		return ""
	}
	return p.sessions[p.current].label
}

// CurrentIndex 返回当前活跃会话的索引。
func (p *Pool) CurrentIndex() int { return p.current }

// Len 返回池中的会话数。
func (p *Pool) Len() int { return len(p.sessions) }

// Summary 是单个会话的摘要信息。
type Summary struct {
	Index     int
	Label     string
	SessionID string
	MsgCount  int
	IsCurrent bool
}

// All 返回所有会话的摘要。
func (p *Pool) All() []Summary {
	result := make([]Summary, len(p.sessions))
	for i, ns := range p.sessions {
		result[i] = Summary{
			Index:     i,
			Label:     ns.label,
			SessionID: ns.session.SessionID(),
			MsgCount:  len(ns.session.History()),
			IsCurrent: i == p.current,
		}
	}
	return result
}

// Chat 在当前活跃会话中对话。
func (p *Pool) Chat(ctx context.Context, input string) (string, error) {
	s := p.Current()
	if s == nil {
		return "", fmt.Errorf("pool is empty, call Add first")
	}
	return s.Chat(ctx, input)
}

// ChatStream 在当前活跃会话中流式对话。
func (p *Pool) ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error) {
	s := p.Current()
	if s == nil {
		return "", fmt.Errorf("pool is empty, call Add first")
	}
	return s.ChatStream(ctx, input, onChunk)
}
