package workplan

import (
	"fmt"
	"strings"
)

// =============================================================================
// validate.go —— WorkPlan 拓扑校验
// =============================================================================
//
// 在 Run 前校验结构合法性，防止无效拓扑和无限循环烧 token。
// Run 内部自动调用 Validate()，调用方也可以提前校验。

// Validate 执行拓扑校验，返回第一个错误。nil 表示合法。
func (wp *WorkPlan) Validate() error {
	if wp.entryID == "" {
		return fmt.Errorf("WorkPlan: no nodes defined")
	}

	// 1. 节点级配置检查
	for _, n := range wp.nodes {
		switch n.kind {
		case kindLoop:
			if n.loopBodyID == "" {
				return fmt.Errorf("WorkPlan: Loop node %q has no body ID", n.id)
			}
			if n.loopUntil == nil && n.loopMaxIter <= 0 {
				return fmt.Errorf("WorkPlan: Loop node %q has neither Until nor MaxIter — this would loop forever", n.id)
			}
		case kindFork:
			if len(n.forkBranches) == 0 {
				return fmt.Errorf("WorkPlan: Fork node %q has no branches", n.id)
			}
		}
	}

	// 2. 引用完整性：所有目标节点必须存在
	for _, n := range wp.nodes {
		for _, tgt := range wp.collectTargets(n) {
			if tgt == "" {
				continue
			}
			if _, ok := wp.nodeIndex[tgt]; !ok {
				return fmt.Errorf("WorkPlan: node %q references unknown node %q", n.id, tgt)
			}
		}
	}

	// 3. 环检测
	return wp.detectCycle()
}

// collectTargets 返回节点引用的所有目标节点 ID。
func (wp *WorkPlan) collectTargets(n *node) []string {
	var targets []string
	if n.next != "" {
		targets = append(targets, n.next)
	}
	switch n.kind {
	case kindIf:
		targets = append(targets, n.ifTrueID, n.ifFalseID)
	case kindSwitch:
		for _, c := range n.switchCases {
			if c.NextID != "" {
				targets = append(targets, c.NextID)
			}
		}
	case kindLoop:
		targets = append(targets, n.loopBodyID)
		if n.loopExhaustedID != "" {
			targets = append(targets, n.loopExhaustedID)
		}
	}
	return targets
}

// cycleEdges 返回环检测用的出边。
// 排除 loopBodyID：Loop 的迭代由 Until/MaxIter 控制，不走图拓扑。
func (wp *WorkPlan) cycleEdges(nodeID string) []string {
	n, ok := wp.nodeIndex[nodeID]
	if !ok {
		return nil
	}
	var edges []string
	if n.next != "" {
		edges = append(edges, n.next)
	}
	switch n.kind {
	case kindIf:
		if n.ifTrueID != "" {
			edges = append(edges, n.ifTrueID)
		}
		if n.ifFalseID != "" {
			edges = append(edges, n.ifFalseID)
		}
	case kindSwitch:
		for _, c := range n.switchCases {
			if c.NextID != "" {
				edges = append(edges, c.NextID)
			}
		}
	case kindLoop:
		// next（循环出口）和 exhausted，不跟 loopBodyID
		if n.loopExhaustedID != "" {
			edges = append(edges, n.loopExhaustedID)
		}
	}
	return edges
}

// detectCycle DFS 三色标记环检测。
func (wp *WorkPlan) detectCycle() error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(wp.nodes))
	path := make([]string, 0)

	var dfs func(nodeID string) error
	dfs = func(nodeID string) error {
		color[nodeID] = gray
		path = append(path, nodeID)

		for _, next := range wp.cycleEdges(nodeID) {
			if next == "" {
				continue
			}
			switch color[next] {
			case gray:
				start := -1
				for i, n := range path {
					if n == next {
						start = i
						break
					}
				}
				cycle := append(path[start:], next)
				return fmt.Errorf("WorkPlan: cycle detected: %s", strings.Join(cycle, " → "))
			case white:
				if err := dfs(next); err != nil {
					return err
				}
			}
		}

		color[nodeID] = black
		path = path[:len(path)-1]
		return nil
	}

	return dfs(wp.entryID)
}
