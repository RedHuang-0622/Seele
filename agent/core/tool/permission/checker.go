package permission

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ─── 检查结果 ──────────────────────────────────────────────────────

// CheckResult 是权限检查的结果。
type CheckResult int

const (
	ResultAllow CheckResult = iota // 允许
	ResultAsk                      // 需要审批
	ResultDeny                     // 拒绝
)

func toResult(a Action) CheckResult {
	switch a {
	case ActionAllow:
		return ResultAllow
	case ActionDeny:
		return ResultDeny
	case ActionAsk:
		return ResultAsk
	default:
		return ResultAllow
	}
}

// ─── 权限检查器 ────────────────────────────────────────────────────

// PermissionChecker 持有一组权限规则，提供工具调用的权限检查能力。
type PermissionChecker struct {
	mu    sync.RWMutex
	rules []PermissionRule
	// 持久化的"始终允许"规则（由用户在运行时选择 Remember 时添加）
	allowCache map[string]bool // key: "toolName:argsPrefix"
}

// NewPermissionChecker 使用给定的配置创建权限检查器。
func NewPermissionChecker(cfg PermissionConfig) *PermissionChecker {
	rules := make([]PermissionRule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	return &PermissionChecker{
		rules:      rules,
		allowCache: make(map[string]bool),
	}
}

// Check 对给定的工具调用进行权限检查，返回检查结果。
// 规则按顺序评估，最后匹配的规则胜出。没有匹配规则时返回 ResultAllow。
func (pc *PermissionChecker) Check(toolName, argsJSON string) CheckResult {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	// 先检查运行时缓存（"始终允许"记忆）
	cacheKey := buildCacheKey(toolName, argsJSON)
	if pc.allowCache[cacheKey] {
		return ResultAllow
	}

	var result CheckResult = ResultAllow
	for _, rule := range pc.rules {
		if !matchGlob(rule.ToolName, toolName) {
			continue
		}
		if len(rule.Patterns) == 0 {
			// 无参数模式：匹配该工具的所有调用
			result = toResult(rule.Action)
			continue
		}
		for _, pattern := range rule.Patterns {
			if matchGlob(pattern, argsJSON) {
				result = toResult(rule.Action)
				break
			}
		}
	}
	return result
}

// AddAllowRule 运行时添加一条"始终允许"的缓存记录。
// 当用户选择 "always allow" 时调用。
func (pc *PermissionChecker) AddAllowRule(toolName, argsJSON string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.allowCache[buildCacheKey(toolName, argsJSON)] = true
}

// SetRules 替换全部权限规则（运行时重新加载配置时使用）。
func (pc *PermissionChecker) SetRules(rules []PermissionRule) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.rules = make([]PermissionRule, len(rules))
	copy(pc.rules, rules)
}

// Rules 返回当前权限规则的副本。
func (pc *PermissionChecker) Rules() []PermissionRule {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	r := make([]PermissionRule, len(pc.rules))
	copy(r, pc.rules)
	return r
}

// ─── 审批回调生成 ─────────────────────────────────────────────────

// NewChannelApprovalHandler 创建一个基于 channel 的审批处理器。
// 适用于 bridge goroutine ↔ TUI 主循环的场景。
func NewChannelApprovalHandler(reqCh chan<- ApprovalRequest) ApprovalHandler {
	return func(ctx *ApprovalContext) (*ApprovalResponse, error) {
		req := ctx.Request

		// 发送请求到 UI 侧
		select {
		case reqCh <- req:
		default:
			return nil, fmt.Errorf("approval: channel full, request %s dropped", req.ID)
		}

		// 等待响应
		respCh := make(chan *ApprovalResponse, 1)
		ctx.Response = respCh

		select {
		case resp := <-respCh:
			if resp == nil {
				return nil, fmt.Errorf("approval: cancelled")
			}
			resp.Timestamp = time.Now()
			return resp, nil
		case <-time.After(req.Timeout):
			if req.Timeout > 0 {
				return nil, fmt.Errorf("approval: timeout after %v", req.Timeout)
			}
			return nil, fmt.Errorf("approval: cancelled")
		}
	}
}

// ─── 辅助函数 ──────────────────────────────────────────────────────

func buildCacheKey(toolName, argsJSON string) string {
	// 只用工具名 + 前 200 字符参数作为缓存键
	prefix := argsJSON
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	return toolName + ":" + prefix
}

// matchGlob 执行简单的 glob 匹配（与 holder/plugin.go 中的版本等价）。
// 支持 * 作为通配符。
func matchGlob(pattern, name string) bool {
	if pattern == "" || name == "" {
		return pattern == name
	}
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}

	parts := strings.Split(pattern, "*")
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return true
	}

	// 前缀匹配
	if strings.HasPrefix(pattern, nonEmpty[0]) && !strings.HasPrefix(pattern, "*") {
		if !strings.HasPrefix(name, nonEmpty[0]) {
			return false
		}
		nonEmpty = nonEmpty[1:]
	}

	// 后缀匹配
	if len(nonEmpty) > 0 && strings.HasSuffix(pattern, nonEmpty[len(nonEmpty)-1]) &&
		!strings.HasSuffix(pattern, "*") {
		last := nonEmpty[len(nonEmpty)-1]
		if !strings.HasSuffix(name, last) {
			return false
		}
		nonEmpty = nonEmpty[:len(nonEmpty)-1]
	}

	// 中间段匹配
	rest := name
	for _, part := range nonEmpty {
		idx := strings.Index(rest, part)
		if idx == -1 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return true
}
