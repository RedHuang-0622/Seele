package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// =============================================================================
// [workplangate] ApprovalGate —— 人工确认接口（Q-K-V 模型）
// =============================================================================
//
// 所有 Gate 实现都通过 Ask 方法阻塞等待用户决策。
//   - CLIApprovalGate: 本地 fmt.Scanln（开发/测试用）
//   - NetworkApprovalGate: OnQuestion 推送 + channel 阻塞（容器/网络用）
//   - AutoApproveGate: 不等待，直接返回默认 V（自动化测试用）
//
// Question 中携带 Q（问题内容）、K（选项列表）、KVS（K→V 映射表）。
// Ask 返回匹配到的 V（any 类型），调用方 switch v 决定后续行为。

// ApprovalGate 人工确认接口。
// Ask 阻塞直到人做出选择或超时/取消，返回匹配到的 V。
type ApprovalGate interface {
	Ask(ctx context.Context, q Question) (any, error)
}

// =============================================================================
// [workplangate] NetworkApprovalGate —— 网络审批（容器环境）
// =============================================================================
//
// 两段式协议：
//   1. Ask 被调用 → 存储 Question → 调用 OnQuestion 推送到外部（gRPC response / WebSocket）
//   2. Ask 阻塞在 channel 上等待 Decide
//   3. 外部收到 Question 展示给用户 → 用户选择 → 调用 Decide 回传 choice
//   4. Ask 的 channel 被解锁 → resolve K→V → 返回 V
//
// OnQuestion 由 Handler 注入，用于将审批请求推回 CLI。

type NetworkApprovalGate struct {
	mu         sync.Mutex
	pending    map[string]chan string // questionID → choice channel
	questions  map[string]Question    // questionID → Question（含 KVS）

	// OnQuestion 推送回调，由调用方（Handler）注入。
	// 当 Ask 被调用时触发，将 Question 推送到 CLI。
	// 注意：此回调在 Ask 的 goroutine 内同步调用，应尽快返回。
	OnQuestion func(q Question) error

	// DefaultTimeout 全局默认超时，Ask 未指定 Question.Timeout 时使用。
	// 0 表示不限时。
	DefaultTimeout time.Duration
}

// NewNetworkApprovalGate 创建网络审批门。
func NewNetworkApprovalGate() *NetworkApprovalGate {
	return &NetworkApprovalGate{
		pending:   make(map[string]chan string),
		questions: make(map[string]Question),
	}
}

// Ask 实现 ApprovalGate。存储 Question → 推送 → 阻塞等 Decide。
func (g *NetworkApprovalGate) Ask(ctx context.Context, q Question) (any, error) {
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

	// 推送到外部
	if g.OnQuestion != nil {
		if err := g.OnQuestion(q); err != nil {
			return nil, fmt.Errorf("network gate: push question: %w", err)
		}
	}

	// 阻塞等待用户决策（管道 + 超时）
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = g.DefaultTimeout
	}

	if timeout > 0 {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		select {
		case choice := <-ch:
			return g.resolve(q, choice)
		case <-ticker.C:
			// 超时走默认选项
			defaultKey := q.DefaultChoice()
			if defaultKey == "" {
				return nil, fmt.Errorf("network gate: question %q timed out and has no default choice", q.ID)
			}
			v, _ := g.resolve(q, defaultKey)
			return v, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case choice := <-ch:
		return g.resolve(q, choice)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Decide 外部调用，传入用户选择的 key 解锁 Ask。
func (g *NetworkApprovalGate) Decide(questionID, choice string) error {
	g.mu.Lock()
	ch, ok := g.pending[questionID]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("question %q not found or already decided", questionID)
	}
	ch <- choice
	return nil
}

// GetQuestion 查询已存储的 Question（供外部 _decide handler 验证选项用）。
func (g *NetworkApprovalGate) GetQuestion(questionID string) (Question, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	q, ok := g.questions[questionID]
	return q, ok
}

// resolve 执行 K→V 匹配。choice 不存在时返回默认选项的 V。
func (g *NetworkApprovalGate) resolve(q Question, choice string) (any, error) {
	if v, ok := q.KVS[choice]; ok {
		return v, nil
	}
	defaultKey := q.DefaultChoice()
	if defaultKey == "" {
		return nil, fmt.Errorf("network gate: unknown choice %q and no default", choice)
	}
	if v, ok := q.KVS[defaultKey]; ok {
		return v, fmt.Errorf("unknown choice %q, fallback to default %q", choice, defaultKey)
	}
	return nil, fmt.Errorf("network gate: unknown choice %q, default %q has no value", choice, defaultKey)
}

// =============================================================================
// [workplangate] CLIApprovalGate —— 本地命令行审批（开发/测试用）
// =============================================================================

// CLIApprovalGate 使用 fmt.Scanln 在本地终端阻塞等用户输入。
// 仅适用于单机开发调试，容器环境请使用 NetworkApprovalGate。
type CLIApprovalGate struct{}

func (g *CLIApprovalGate) Ask(ctx context.Context, q Question) (any, error) {
	// 美化 Question 输出
	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  [需要确认] %s\n", q.ID)
	fmt.Println(strings.Repeat("─", 60))

	// 尝试美化 plan JSON
	planStr := q.Content
	var planJSON any
	if json.Unmarshal([]byte(q.Content), &planJSON) == nil {
		if pretty, err := json.MarshalIndent(planJSON, "  ", "  "); err == nil {
			planStr = string(pretty)
		}
	}
	fmt.Println("  计划：")
	for _, line := range strings.Split(planStr, "\n") {
		fmt.Printf("    %s\n", line)
	}

	fmt.Println(strings.Repeat("─", 60))
	for i, opt := range q.Options {
		desc := opt.Description
		if desc != "" {
			desc = " — " + desc
		}
		fmt.Printf("  [%d] %s%s\n", i+1, opt.Label, desc)
	}
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print("  输入选项编号或 key: ")

	// 管道 + 超时
	inputCh := make(chan string, 1)
	go func() {
		var s string
		fmt.Scanln(&s)
		inputCh <- strings.TrimSpace(s)
	}()

	select {
	case raw := <-inputCh:
		key := resolveInput(raw, q.Options)
		v, _ := q.Resolve(key)
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resolveInput 将用户原始输入（数字编号或 key 文本）映射为 option key。
func resolveInput(raw string, options []ChoiceOption) string {
	if raw == "" {
		if len(options) > 0 {
			return options[0].Key
		}
		return ""
	}
	// 数字索引
	for i, opt := range options {
		if raw == fmt.Sprintf("%d", i+1) {
			return opt.Key
		}
	}
	// 文本精确匹配 key
	for _, opt := range options {
		if strings.EqualFold(raw, opt.Key) {
			return opt.Key
		}
	}
	// 文本匹配 label
	for _, opt := range options {
		if strings.EqualFold(raw, opt.Label) {
			return opt.Key
		}
	}
	// 无法识别，走默认
	if len(options) > 0 {
		fmt.Printf("[无法识别 %q，使用默认选项 %s]\n", raw, options[0].Label)
		return options[0].Key
	}
	return raw
}

// =============================================================================
// [workplangate] AutoApproveGate —— 自动确认（非交互式测试用）
// =============================================================================

// AutoApproveGate 不等待用户输入，直接返回第一个选项的 V。
type AutoApproveGate struct{}

func (g *AutoApproveGate) Ask(ctx context.Context, q Question) (any, error) {
	if len(q.Options) == 0 {
		return nil, nil
	}
	v, _ := q.Resolve(q.Options[0].Key)
	fmt.Printf("[自动确认] %s → 选择: %s\n", q.ID, q.Options[0].Label)
	return v, nil
}
