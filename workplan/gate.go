package workplan

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/sugar/approve"
)

// ─── CLI Approval Gate ────────────────────────────────────────────────

// CLIApprovalGate implements the ApprovalGate interface for terminal interactions.
type CLIApprovalGate struct{}

// NewCLIApprovalGate creates a CLI-based approval gate.
func NewCLIApprovalGate() *CLIApprovalGate { return &CLIApprovalGate{} }

// Ask presents the question on the terminal and reads a choice.
func (g *CLIApprovalGate) Ask(ctx context.Context, q approve.Question) (any, error) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  [需要确认] %s\n", q.ID)
	fmt.Println(strings.Repeat("─", 60))
	for _, line := range strings.Split(q.Content, "\n") {
		fmt.Printf("    %s\n", line)
	}
	fmt.Println(strings.Repeat("─", 60))
	for i, opt := range q.Options {
		fmt.Printf("  [%d] %s — %s\n", i+1, opt.Label, opt.Description)
	}
	fmt.Print("  输入编号或 key: ")

	inputCh := make(chan string, 1)
	go func() {
		var s string
		fmt.Scanln(&s)
		inputCh <- strings.TrimSpace(s)
	}()
	select {
	case raw := <-inputCh:
		key := q.DefaultChoice()
		for i, opt := range q.Options {
			if raw == fmt.Sprintf("%d", i+1) || raw == opt.Key {
				key = opt.Key
				break
			}
		}
		v, _ := q.Resolve(key)
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ─── Network Approval Gate ────────────────────────────────────────────

// NetworkApprovalGate implements a two-stage approval: push + channel block.
type NetworkApprovalGate struct {
	mu             sync.Mutex
	pending        map[string]chan string
	questions      map[string]approve.Question
	OnQuestion     func(approve.Question) error
	DefaultTimeout time.Duration
}

// NewNetworkApprovalGate creates a network-based approval gate.
func NewNetworkApprovalGate() *NetworkApprovalGate {
	return &NetworkApprovalGate{
		pending:   make(map[string]chan string),
		questions: make(map[string]approve.Question),
	}
}

// Ask pushes the question and waits for a decision.
func (g *NetworkApprovalGate) Ask(ctx context.Context, q approve.Question) (any, error) {
	ch := make(chan string, 1)
	g.mu.Lock()
	g.questions[q.ID] = q
	g.pending[q.ID] = ch
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.pending, q.ID)
		delete(g.questions, q.ID)
		g.mu.Unlock()
	}()

	if g.OnQuestion != nil {
		if err := g.OnQuestion(q); err != nil {
			return nil, err
		}
	}

	timeout := q.Timeout
	if timeout <= 0 {
		timeout = g.DefaultTimeout
	}
	if timeout > 0 {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		select {
		case choice := <-ch:
			v, _ := q.Resolve(choice)
			return v, nil
		case <-ticker.C:
			v, _ := q.Resolve(q.DefaultChoice())
			return v, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case choice := <-ch:
		v, _ := q.Resolve(choice)
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Decide sends a choice response for a pending question.
func (g *NetworkApprovalGate) Decide(questionID, choice string) error {
	g.mu.Lock()
	ch, ok := g.pending[questionID]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("question %q not found", questionID)
	}
	ch <- choice
	return nil
}

// ─── Auto Approval Gate ──────────────────────────────────────────────

// AutoApproveGate automatically approves without waiting.
type AutoApproveGate struct{}

// Ask returns the first option immediately.
func (g *AutoApproveGate) Ask(ctx context.Context, q approve.Question) (any, error) {
	if len(q.Options) == 0 {
		return nil, nil
	}
	v, _ := q.Resolve(q.Options[0].Key)
	return v, nil
}
