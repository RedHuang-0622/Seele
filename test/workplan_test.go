// test/workplan_test.go
package test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan"
)

// =============================================================================
// 测试 1：Fork 支持多 Agent 不同工具注册表
// =============================================================================

func TestWorkPlan_Fork_DifferentRegistries(t *testing.T) {
	llmSrv := newMockLLMServer()
	defer llmSrv.Close()
	llmSrv.EnqueueText(`"前端 完成了任务: 实现 tool_a 功能"`)
	llmSrv.EnqueueText(`"后端 完成了任务: 实现 tool_b 功能"`)

	fa := newSessionFactory(newTestTools(llmSrv.URL()))
	fb := newSessionFactory(newTestTools(llmSrv.URL()))

	provA := newMockProvider("provA")
	provA.AddTool("tool_a", "only for agent A")
	fa.Tools.Register(provA)

	provB := newMockProvider("provB")
	provB.AddTool("tool_b", "only for agent B")
	fb.Tools.Register(provB)

	// 验证工具注册隔离（通过 tool_holder.Tools 检查，而非旧 HasTool）
	toolsA := fa.Tools.Tools()
	hasA, hasBinA := false, false
	for _, t := range toolsA {
		if t.Function.Name == "tool_a" { hasA = true }
		if t.Function.Name == "tool_b" { hasBinA = true }
	}
	toolsB := fb.Tools.Tools()
	hasB, hasAinB := false, false
	for _, t := range toolsB {
		if t.Function.Name == "tool_b" { hasB = true }
		if t.Function.Name == "tool_a" { hasAinB = true }
	}
	if hasA && hasB && !hasBinA && !hasAinB {
		t.Log("OK: 两个工具注册表互不重叠")
	} else {
		t.Errorf("FAIL: 工具注册表未正确隔离 hasA=%v hasB=%v hasBinA=%v hasAinB=%v", hasA, hasB, hasBinA, hasAinB)
	}

	factory := &forkRegFactory{
		factories: map[string]*sessionFactory{
			"前端": fa,
			"后端": fb,
		},
	}

	wp := workplan.New(factory, nil, "")
	wp.Fork("并发任务", []workplan.ForkBranch{
		{Label: "前端", SystemPrompt: "label:前端 你是前端工程师", Input: "实现 tool_a 功能"},
		{Label: "后端", SystemPrompt: "label:后端 你是后端工程师", Input: "实现 tool_b 功能"},
	})

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("WorkPlan.Run: %v", err)
	}

	t.Logf("Fork 执行结果: %s", result.FinalOutputString())
	t.Logf("节点数: %d, 中止: %v", len(result.NodeResults), result.Aborted)

	if !factory.calledA || !factory.calledB {
		t.Errorf("未覆盖所有分支: calledA=%v calledB=%v", factory.calledA, factory.calledB)
	} else {
		t.Log("OK: Fork 两个分支的 Agent 分别来自不同的工具注册表")
	}
}

// forkRegFactory 按 SystemPrompt 中的 label 前缀路由到不同的 sessionFactory。
type forkRegFactory struct {
	mu        sync.Mutex
	factories map[string]*sessionFactory
	calledA   bool
	calledB   bool
}

func (f *forkRegFactory) NewAgent(systemPrompt string) workplan.Agent {
	label := "default"
	if strings.HasPrefix(systemPrompt, "label:") {
		rest := systemPrompt[len("label:"):]
		if idx := strings.Index(rest, " "); idx > 0 {
			label = rest[:idx]
		} else {
			label = rest
		}
	}

	f.mu.Lock()
	switch label {
	case "前端":
		f.calledA = true
	case "后端":
		f.calledB = true
	}
	f.mu.Unlock()

	sf := f.factories[label]
	if sf == nil {
		for _, s := range f.factories {
			sf = s
			break
		}
	}
	return sf.NewAgent(systemPrompt)
}

// =============================================================================
// 测试 2：Loop-Signal-Emit 机制
// =============================================================================

func TestWorkPlan_LoopSignalEmit(t *testing.T) {
	llmSrv := newMockLLMServer()
	defer llmSrv.Close()

	llmSrv.EnqueueText(`"问题1：CPU负载过高 —— 已重启"`)
	llmSrv.EnqueueText(`"问题2：内存泄漏 —— 已清理"`)
	llmSrv.EnqueueText(`"问题3：配置错误 —— 已修正"`)
	llmSrv.EnqueueText(`"问题4：连接池耗尽 —— 已扩容"`)
	llmSrv.EnqueueText(`"问题5：缓存穿透 —— 已加锁，系统恢复正常"`)
	llmSrv.EnqueueText(`"系统已完全恢复正常"`)
	llmSrv.EnqueueText(`"收尾完成"`)

	factory := newSessionFactory(newTestTools(llmSrv.URL()))

	var mu sync.Mutex
	iterResults := make([]string, 0)

	wp := workplan.New(factory, nil, "")

	wp.Auto("初始分析", "分析系统状态")
	wp.Emit("保存分析", "root_cause")
	wp.Auto("修复执行体", "根据上次结果继续修复：{{.PrevResult}}")

	sig := wp.Loop("修复循环", "修复执行体",
		workplan.Until(func(result string) bool {
			return strings.Contains(result, "恢复正常")
		}),
		workplan.MaxIter(5),
		workplan.OnExhausted("人工介入"),
	)

	var signalValues []string
	sig.OnUpdate(func(jsonVal string) {
		mu.Lock()
		signalValues = append(signalValues, jsonVal)
		iterResults = append(iterResults, jsonVal)
		mu.Unlock()
	})

	wp.Auto("人工介入", "生成人工介入告警")
	wp.Auto("完成通知", "根因: {{.Vars.root_cause}}，修复完成")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("WorkPlan.Run: %v", err)
	}

	t.Run("Signal_OnUpdate_called", func(t *testing.T) {
		if len(signalValues) < 2 {
			t.Errorf("expected >=2 OnUpdate callbacks, got %d: %v", len(signalValues), signalValues)
		} else {
			t.Logf("OnUpdate 被触发了 %d 次: %v", len(signalValues), signalValues)
		}
	})

	t.Run("Signal_Get_returns_last", func(t *testing.T) {
		final := sig.GetString()
		if !strings.Contains(final, "恢复正常") {
			t.Errorf("Signal 最终值应包含'恢复正常', got: %s", final)
		}
		t.Logf("Signal 最终值: %s", final)
	})

	t.Run("Emit_variable_accessible", func(t *testing.T) {
		val, ok := result.Vars["root_cause"]
		if !ok || val == "" {
			t.Errorf("Emit 变量 root_cause 未写入或为空: ok=%v val=%q", ok, val)
		}
		t.Logf("Emit root_cause: %s", val)
	})

	t.Run("Loop_exits_on_until", func(t *testing.T) {
		if sig.Iter() > 3 {
			t.Errorf("Loop 应该在 Until 满足时提前退出, iter=%d", sig.Iter())
		}
		t.Logf("Loop 迭代次数: %d", sig.Iter())
	})

	t.Run("final_output", func(t *testing.T) {
		out := result.FinalOutputString()
		t.Logf("最终输出: %s", out)
		if out == "" {
			t.Error("最终输出为空")
		}
	})

	t.Run("node_results", func(t *testing.T) {
		for _, nr := range result.NodeResults {
			status := "✓"
			if nr.Skipped {
				status = "⏭"
			}
			t.Logf("  %s %-12s %-20s", status, nr.Kind, nr.NodeID)
		}
	})
}

// =============================================================================
// 测试 3：Loop 耗尽后的 exhausted 路径
// =============================================================================

func TestWorkPlan_LoopExhausted(t *testing.T) {
	llmSrv := newMockLLMServer()
	defer llmSrv.Close()

	for i := 0; i < 10; i++ {
		llmSrv.EnqueueText(`"故障仍未恢复，还需继续修复"`)
	}

	factory := newSessionFactory(newTestTools(llmSrv.URL()))

	wp := workplan.New(factory, nil, "")

	wp.Auto("修复执行体", "执行修复: {{.PrevResult}}")

	sig := wp.Loop("重试循环", "修复执行体",
		workplan.Until(func(s string) bool { return strings.Contains(s, "已恢复") }),
		workplan.MaxIter(3),
		workplan.OnExhausted("降级处理"),
	)

	wp.Auto("降级处理", "进入降级流程")
	wp.Auto("正常结束", "收尾工作")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Run("exhausted_path_taken", func(t *testing.T) {
		if sig.Iter() != 3 {
			t.Errorf("expected 3 iterations, got %d", sig.Iter())
		}

		foundDegrade := false
		for _, nr := range result.NodeResults {
			if nr.NodeID == "降级处理" && !nr.Skipped {
				foundDegrade = true
			}
		}
		if !foundDegrade {
			t.Error("降级处理节点应该被执行（exhausted 路径）")
		} else {
			t.Log("OK: exhausted 路径正确跳转到降级处理，之后继续到正常结束")
		}
	})
}

// =============================================================================
// 测试 4：Emit 变量在 Fork 分支中的引用
// =============================================================================

func TestWorkPlan_EmitInFork(t *testing.T) {
	llmSrv := newMockLLMServer()
	defer llmSrv.Close()

	llmSrv.EnqueueText(`"处理完成: 分析需求：用户登录功能"`)
	llmSrv.EnqueueText(`"前端处理完成: 实现登录页面"`)
	llmSrv.EnqueueText(`"后端处理完成: 实现登录接口"`)

	factory := newSessionFactory(newTestTools(llmSrv.URL()))

	wp := workplan.New(factory, nil, "")

	wp.Auto("需求分析", "分析需求：用户登录功能").
		Emit("保存需求", "requirement")

	wp.Fork("并行开发", []workplan.ForkBranch{
		{Label: "前端", SystemPrompt: "你是前端工程师", Input: "实现登录页面，需求：{{.Vars.requirement}}"},
		{Label: "后端", SystemPrompt: "你是后端工程师", Input: "实现登录接口，需求：{{.Vars.requirement}}"},
	})

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Run("vars_populated", func(t *testing.T) {
		val, ok := result.Vars["requirement"]
		if !ok || val == "" {
			t.Error("Emit 变量 requirement 未写入")
		}
		t.Logf("requirement = %s", val)
	})

	t.Run("fork_output_is_object", func(t *testing.T) {
		out := result.FinalOutput()
		if out == "" || out == `""` {
			t.Error("Fork 输出不应为空")
		}
		t.Logf("Fork 汇合结果: %.200s", out)
	})
}
