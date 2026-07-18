package permission

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type CheckResult int

const (
	ResultAllow CheckResult = iota
	ResultAsk
	ResultDeny
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

type PermissionChecker struct {
	mu         sync.RWMutex
	mode       Mode
	rules      []PermissionRule
	allowCache map[string]bool
}

func NewPermissionChecker(cfg PermissionConfig) *PermissionChecker {
	rules := make([]PermissionRule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	return &PermissionChecker{
		mode:       cfg.EffectiveMode(),
		rules:      rules,
		allowCache: make(map[string]bool),
	}
}

// SetMode 运行时切换权限模式（每次 Check 都会读取最新值）。
func (pc *PermissionChecker) SetMode(m Mode) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.mode = m
}

// Mode 返回当前权限模式。
func (pc *PermissionChecker) Mode() Mode {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.mode
}

func (pc *PermissionChecker) Check(toolName, argsJSON string) CheckResult {
	pc.mu.RLock()
	mode := pc.mode
	defer pc.mu.RUnlock()

	// full_access：所有工具静默放行
	if mode == ModeFullAccess {
		return ResultAllow
	}

	cacheKey := toolName + ":" + truncateStr(argsJSON, 200)
	if pc.allowCache[cacheKey] {
		return ResultAllow
	}

	// manual：默认 ask，由规则覆盖
	var result CheckResult = ResultAsk
	for _, rule := range pc.rules {
		if !matchGlob(rule.ToolName, toolName) {
			continue
		}
		if len(rule.Patterns) == 0 {
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

func (pc *PermissionChecker) AddAllowRule(toolName, argsJSON string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.allowCache[toolName+":"+truncateStr(argsJSON, 200)] = true
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func NewChannelApprovalHandler(reqCh chan<- ApprovalRequest) ApprovalHandler {
	return func(ctx *ApprovalContext) (*ApprovalResponse, error) {
		req := ctx.Request
		select {
		case reqCh <- req:
		default:
			return nil, fmt.Errorf("approval: channel full, request dropped")
		}
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
	if strings.HasPrefix(pattern, nonEmpty[0]) && !strings.HasPrefix(pattern, "*") {
		if !strings.HasPrefix(name, nonEmpty[0]) {
			return false
		}
		nonEmpty = nonEmpty[1:]
	}
	if len(nonEmpty) > 0 && strings.HasSuffix(pattern, nonEmpty[len(nonEmpty)-1]) &&
		!strings.HasSuffix(pattern, "*") {
		last := nonEmpty[len(nonEmpty)-1]
		if !strings.HasSuffix(name, last) {
			return false
		}
		nonEmpty = nonEmpty[:len(nonEmpty)-1]
	}
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
