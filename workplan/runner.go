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
// autoRunner —— Agent ReAct 循环节点
// =============================================================================

type autoRunner struct {
	id            string
	systemPrompt  string
	input         string
	toolFilter    []string
	factory       AgentFactory
	defaultPrompt string // 从 WorkPlan 继承的默认 prompt，systemPrompt 为空时使用
}

func (r *autoRunner) ID() string { return r.id }

func (r *autoRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	input := renderTemplate(r.input, ec)
	prompt := r.systemPrompt
	if prompt == "" {
		prompt = r.defaultPrompt
	}
	agent := r.factory.NewAgent(prompt)
	if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && len(r.toolFilter) > 0 {
		f.SetToolFilter(r.toolFilter)
	}
	out, err := agent.Chat(ctx, input)
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}

// =============================================================================
// controlRunner —— 控制节点（If / Switch / Gate 等透传节点）
// =============================================================================

// controlRunner 不执行 Agent，只透传 ec.PrevOutput。
// 路由逻辑完全在 Edge.Condition 中处理。
type controlRunner struct {
	id   string
	kind string // "if" | "switch" | "gate"
}

func (r *controlRunner) ID() string { return r.id }

func (r *controlRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	return ec.PrevOutput, nil
}

// =============================================================================
// loopRunner —— 循环节点（Signal 机制）
// =============================================================================

type loopRunner struct {
	id         string
	bodyRunner NodeRunner  // 循环体 runner（autoRunner，每次迭代创建新 session）
	until      func(string) bool
	maxIter    int
	signal     *Signal

	// 构建期缓存：循环体的配置
	bodyPrompt     string
	bodyInput      string
	factory        AgentFactory
	defaultPrompt  string
}

func (r *loopRunner) ID() string { return r.id }

func (r *loopRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	if r.signal == nil {
		r.signal = newSignal()
	}
	defer r.signal.close()

	current := ec.PrevOutput
	for iter := 0; ; iter++ {
		select {
		case <-ctx.Done():
			return r.signal.Get(), ctx.Err()
		default:
		}

		input := renderTemplate(r.bodyInput, ec)
		// 把上次迭代的结果注入模板（当前循环的输出是下次的输入）
		if iter > 0 {
			input = renderTemplate(r.bodyInput, &ExecutionContext{
				PrevOutput: current,
				Vars:       ec.Vars,
			})
		}
		prompt := r.bodyPrompt
		if prompt == "" {
			prompt = r.defaultPrompt
		}
		agent := r.factory.NewAgent(prompt)
		out, err := agent.Chat(ctx, input)
		if err != nil {
			return r.signal.Get(), fmt.Errorf("loop iter %d: %w", iter, err)
		}

		r.signal.set(out, iter+1)
		current = out

		// 退出条件：until 函数
		if r.until != nil && r.until(fromJSON(out)) {
			break
		}
		// 退出条件：最大迭代次数
		if r.maxIter > 0 && iter+1 >= r.maxIter {
			break
		}
	}
	return r.signal.Get(), nil
}

// =============================================================================
// forkRunner —— 并发多 Agent 节点
// =============================================================================

type forkRunner struct {
	id            string
	branches      []ForkBranch
	factory       AgentFactory
	defaultPrompt string
	maxConcurrent int // Fork 最大并发分支数，默认 3
}

func (r *forkRunner) ID() string { return r.id }

func (r *forkRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	type branchResult struct {
		label string
		out   string
		err   error
	}

	results := make([]branchResult, len(r.branches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, r.maxConcurrent)

	for i, branch := range r.branches {
		wg.Add(1)
		go func(i int, b ForkBranch) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[i] = branchResult{label: b.Label, err: fmt.Errorf("branch panic: %v", r)}
				}
			}()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}

			input := renderTemplate(b.Input, ec)
			prompt := b.SystemPrompt
			if prompt == "" {
				prompt = r.defaultPrompt
			}
			agent := r.factory.NewAgent(prompt)
			if agent == nil {
				results[i] = branchResult{label: b.Label, err: fmt.Errorf("factory returned nil agent")}
				return
			}
			out, err := agent.Chat(ctx, input)
			if err != nil {
				results[i] = branchResult{label: b.Label, err: err}
				return
			}
			results[i] = branchResult{label: b.Label, out: toJSON(out)}
		}(i, branch)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	merged := make(map[string]interface{}, len(results))
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", r.label, r.err))
			merged[r.label] = nil
			continue
		}
		var v interface{}
		if err := json.Unmarshal([]byte(r.out), &v); err == nil {
			merged[r.label] = v
		} else {
			merged[r.label] = r.out
		}
	}
	if len(errs) > 0 && len(merged) == 0 {
		return "", fmt.Errorf("all fork branches failed: %s", strings.Join(errs, "; "))
	}

	b, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("fork: marshal result: %w", err)
	}
	return string(b), nil
}

// =============================================================================
// checkpointRunner —— 快照节点
// =============================================================================

type checkpointRunner struct {
	id string
}

func (r *checkpointRunner) ID() string { return r.id }

func (r *checkpointRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	ec.Result.Checkpoints[r.id] = ec.PrevOutput
	return ec.PrevOutput, nil
}

// =============================================================================
// emitRunner —— 命名变量写入节点
// =============================================================================

type emitRunner struct {
	id  string
	key string
}

func (r *emitRunner) ID() string { return r.id }

func (r *emitRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	ec.Vars[r.key] = ec.PrevOutput
	return ec.PrevOutput, nil
}

// =============================================================================
// approveRunner —— 人工确认节点（pause/resume 由 WorkPlan.Run 拦截处理）
// =============================================================================

// approveRunner 不通过 graph.Execute 自动执行；WorkPlan.Run() 检测到 approve 节点后
// 在调用 runner.Run() 之前暂停。Run() 方法仅在 Resume 后执行已批准的动作。
type approveRunner struct {
	id            string
	systemPrompt  string
	input         string
	options       []ChoiceOption
	kvs           map[string]any
	timeout       time.Duration
	factory       AgentFactory
	defaultPrompt string

	// wp 引用父 WorkPlan，用于 prepareApprove / executeApprove
	wp *WorkPlan
}

func (r *approveRunner) ID() string { return r.id }

// Run 在 Resume 时通过 executeApprove 间接调用。
// 正常 Run() 中的 approve 节点不会走到这里——WorkPlan.Run() 提前拦截。
func (r *approveRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
	action, _ := r.wp.pauseDecision.(string)
	switch ApproveChoice(action) {
	case ChoiceSkip:
		return ec.PrevOutput, nil
	case ChoiceAbort:
		return ec.PrevOutput, nil
	default:
		// execute
		input := renderTemplate(r.input, ec)
		prompt := r.systemPrompt
		if prompt == "" {
			prompt = r.defaultPrompt
		}
		agent := r.factory.NewAgent(prompt)
		out, err := agent.Chat(ctx, input)
		if err != nil {
			return "", fmt.Errorf("executeApprove: %w", err)
		}
		return toJSON(out), nil
	}
}

// =============================================================================
// 共享工具函数
// =============================================================================

// renderTemplate 渲染输入模板中的变量。
// 支持 {{.PrevResult}} 和 {{.Vars.key}}。
func renderTemplate(tmpl string, ec *ExecutionContext) string {
	if ec == nil {
		return tmpl
	}
	result := strings.ReplaceAll(tmpl, "{{.PrevResult}}", fromJSON(ec.PrevOutput))
	for key, jsonVal := range ec.Vars {
		result = strings.ReplaceAll(result, "{{.Vars."+key+"}}", fromJSON(jsonVal))
	}
	return result
}
