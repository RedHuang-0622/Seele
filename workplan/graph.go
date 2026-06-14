package workplan

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// =============================================================================
// Graph —— 图引擎
// =============================================================================

// Graph 是 WorkPlan 的底层图执行引擎。
// 持有节点和边，负责执行遍历和路由。
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]NodeRunner
	edges []Edge
	entry string
}

// NewGraph 创建空图。
func NewGraph() *Graph {
	return &Graph{nodes: make(map[string]NodeRunner)}
}

// AddNode 注册一个节点。
func (g *Graph) AddNode(runner NodeRunner) {
	g.mu.Lock()
	g.nodes[runner.ID()] = runner
	g.mu.Unlock()
}

// AddEdge 注册一条边。
func (g *Graph) AddEdge(e Edge) {
	g.mu.Lock()
	g.edges = append(g.edges, e)
	g.mu.Unlock()
}

// SetEntry 设置入口节点 ID。
func (g *Graph) SetEntry(nodeID string) { g.entry = nodeID }

// NodeIDs 返回所有节点 ID（调试用）。
func (g *Graph) NodeIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	return ids
}

// GetNode 返回指定 ID 的 NodeRunner。
func (g *Graph) GetNode(id string) NodeRunner {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.nodes[id]
}

// =============================================================================
// Edge —— 一等公民边
// =============================================================================

// Edge 表示图的一条有向边。
type Edge struct {
	From      string         // 源节点 ID
	To        string         // 目标节点 ID
	Condition EdgeCondition  // nil = 无条件边，直接跟随
	Priority  int            // 条件边之间的优先级，0 最高
	Label     string         // 标签（调试用），如 "true"、"false"、"exhausted"
}

// EdgeCondition 是边的触发条件。
type EdgeCondition func(ec *ExecutionContext) bool

// =============================================================================
// ExecutionContext —— 图执行期间的共享状态
// =============================================================================

// ExecutionContext 在图执行期间传递共享状态。
type ExecutionContext struct {
	Vars       map[string]string // Emit 写入的命名变量
	PrevOutput string            // 上一节点的 JSON 输出
	Result     *WorkPlanResult   // 累积执行结果
	Metadata   map[string]any    // 扩展字段
}

// NewExecutionContext 创建空的执行上下文。
func NewExecutionContext() *ExecutionContext {
	return &ExecutionContext{
		Vars:     make(map[string]string),
		Result:   &WorkPlanResult{Checkpoints: make(map[string]string)},
		Metadata: make(map[string]any),
	}
}

// =============================================================================
// NodeRunner —— 可执行节点的最小抽象
// =============================================================================

// NodeRunner 是可执行节点的接口。
// 所有糖方法内部构造对应的 Runner 实现。
type NodeRunner interface {
	ID() string
	Run(ctx context.Context, ec *ExecutionContext) (output string, err error)
}

// =============================================================================
// Execute —— 图执行引擎入口
// =============================================================================

// Execute 从入口节点开始执行图，直到没有可跟随的边。
func (g *Graph) Execute(ctx context.Context, ec *ExecutionContext) error {
	if g.entry == "" {
		return fmt.Errorf("graph: no entry node")
	}
	return g.executeFrom(ctx, g.entry, ec)
}

// ExecuteFrom 从指定节点开始执行图（用于 Resume 恢复场景）。
func (g *Graph) ExecuteFrom(ctx context.Context, startNodeID string, ec *ExecutionContext) error {
	return g.executeFrom(ctx, startNodeID, ec)
}

func (g *Graph) executeFrom(ctx context.Context, startNodeID string, ec *ExecutionContext) error {
	current := startNodeID
	for current != "" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		node, ok := g.nodes[current]
		if !ok {
			return fmt.Errorf("graph: node %q not found", current)
		}

		output, err := node.Run(ctx, ec)
		if err != nil {
			return fmt.Errorf("graph: node %q: %w", current, err)
		}
		ec.PrevOutput = output

		next := g.resolve(current, ec)
		current = next
	}
	return nil
}

// =============================================================================
// resolve —— 统一路由
// =============================================================================

// resolve 从当前节点出发，按优先级找到第一条匹配的边。
func (g *Graph) resolve(currentID string, ec *ExecutionContext) string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var candidates []Edge
	for _, e := range g.edges {
		if e.From == currentID {
			candidates = append(candidates, e)
		}
	}

	// 无条件边：直接返回
	for _, e := range candidates {
		if e.Condition == nil {
			return e.To
		}
	}

	// 条件边：按优先级排序后依次匹配
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority < candidates[j].Priority
	})
	for _, e := range candidates {
		if e.Condition(ec) {
			return e.To
		}
	}

	return "" // 没有出边 = 图结束
}

// =============================================================================
// Validate —— 图校验
// =============================================================================

// Validate 校验图的完整性。
func (g *Graph) Validate() error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// 入口节点存在性
	if _, ok := g.nodes[g.entry]; g.entry != "" && !ok {
		return fmt.Errorf("entry node %q not found", g.entry)
	}

	// 边引用完整性
	for _, e := range g.edges {
		if _, ok := g.nodes[e.To]; !ok {
			return fmt.Errorf("edge %q → %q: target node %q not found", e.From, e.To, e.To)
		}
	}

	return nil
}
