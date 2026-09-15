package permission

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
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

	// v0.3.0 Linux 式模型字段，全部可选；零值等价于旧的纯规则行为。
	groups     []PermissionGroup
	subjects   map[Subject]SubjectGrant
	missingBit Action
	elevations map[Subject]*elevationState
	auditor    ElevationAuditor

	// elevationSource 是 harness 注入的提权台账。非 nil 时提权的读写都走它，
	// 框架不再使用内置 elevations。注入的实现不得回调 PermissionChecker。
	elevationSource Elevator
}

// elevationState 保存按 scope 记录的提权结果，供后续调用复用。
type elevationState struct {
	groups map[string]bool // ScopeSession：subject+group
	tools  map[string]bool // ScopeTool：subject+tool
	args   map[string]bool // ScopeArgs：subject+tool+args
}

func NewPermissionChecker(cfg PermissionConfig) *PermissionChecker {
	rules := make([]PermissionRule, len(cfg.Rules))
	copy(rules, cfg.Rules)
	groups := make([]PermissionGroup, len(cfg.Groups))
	copy(groups, cfg.Groups)
	var subjects map[Subject]SubjectGrant
	if len(cfg.Subjects) > 0 {
		subjects = make(map[Subject]SubjectGrant, len(cfg.Subjects))
		for subject, grant := range cfg.Subjects {
			subjects[subject] = grant
		}
	}
	return &PermissionChecker{
		mode:       cfg.EffectiveMode(),
		rules:      rules,
		allowCache: make(map[string]bool),
		groups:     groups,
		subjects:   subjects,
		missingBit: cfg.MissingBit,
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

// Check 是旧入口：等价于 CheckFor(SubjectAnonymous, ...)，行为与历史一致。
func (pc *PermissionChecker) Check(toolName, argsJSON string) CheckResult {
	return pc.CheckFor(SubjectAnonymous, toolName, argsJSON)
}

// CheckFor 以指定主体判定调用结果。求值顺序：full_access 放行 → allow 缓存
// 放行 → 路由到组 → 判位（缺位走 MissingBit，零值等价 ask）→ 套用 Rules。
func (pc *PermissionChecker) CheckFor(subject Subject, toolName, argsJSON string) CheckResult {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pc.mode == ModeFullAccess {
		return ResultAllow
	}
	if pc.allowCache[toolName+":"+truncateStr(argsJSON, 200)] {
		return ResultAllow
	}
	result, _ := pc.decideLocked(subject, toolName, argsJSON)
	return result
}

// VisibleFor 报告工具对主体是否可路由（持有的位是否满足所属组）。位不足即
// “不在 PATH”。未配置 Subjects、工具未路由到任何组、或 full_access 时恒为 true。
func (pc *PermissionChecker) VisibleFor(subject Subject, toolName string) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pc.mode == ModeFullAccess || len(pc.subjects) == 0 {
		return true
	}
	_, visible := pc.decideLocked(subject, toolName, "")
	return visible
}

// SetElevationAuditor 安装提权审计回调。传 nil 关闭审计。
func (pc *PermissionChecker) SetElevationAuditor(auditor ElevationAuditor) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.auditor = auditor
}

// Elevator 是提权的挂载点（接口即契约）：框架只调用它，提权台账与策略由
// harness 持有——是否持久、按什么范围复用、如何落盘与呈现，都由 harness 决定。
// 框架自带一个寄存在 PermissionChecker 上的默认实现（CheckerElevator）。
type Elevator interface {
	// Elevate 记录一次已授予的提权；scope 语义见 Scope* 常量。
	Elevate(subject Subject, scope string, toolName, argsJSON string)
	// Elevated 报告是否存在覆盖本次调用的提权。
	Elevated(subject Subject, toolName, argsJSON string) bool
}

// CheckerElevator 是 Elevator 的默认实现：提权寄存在 PermissionChecker 自身的
// 内存台账上，行为与历史一致，供未安装自有台账的 harness 直接使用。
type CheckerElevator struct{ Checker *PermissionChecker }

func (e CheckerElevator) Elevate(subject Subject, scope string, toolName, argsJSON string) {
	if e.Checker != nil {
		e.Checker.GrantElevation(subject, scope, toolName, argsJSON)
	}
}

func (e CheckerElevator) Elevated(subject Subject, toolName, argsJSON string) bool {
	return e.Checker != nil && e.Checker.Elevated(subject, toolName, argsJSON)
}

// SetElevationSource 安装 harness 自己的提权台账。安装后判定读取与提权写入都走它，
// 框架不再读写内置台账；传 nil 恢复内置台账。注入的实现不得回调 PermissionChecker
// （判定在读锁内查询台账）。
func (pc *PermissionChecker) SetElevationSource(source Elevator) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.elevationSource = source
}

// Elevator 返回当前生效的提权台账：harness 注入的，或内置的 CheckerElevator。
// 中间件用它在审批通过后记录提权，框架本身不定义提权策略。
func (pc *PermissionChecker) Elevator() Elevator {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	if pc.elevationSource != nil {
		return pc.elevationSource
	}
	return CheckerElevator{Checker: pc}
}

// Elevated 报告内置台账中是否存在覆盖本次调用的提权（与判定同源，供 harness 查询）。
func (pc *PermissionChecker) Elevated(subject Subject, toolName, argsJSON string) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.elevatedLocked(subject, "", toolName, argsJSON)
}

// GrantElevation 记录一次已授予的提权，按 Scope 决定是否复用到后续调用，并
// 在每次提权时发出审计事件。Scope 为空等价于 ScopeOnce（不放宽后续调用）。
func (pc *PermissionChecker) GrantElevation(subject Subject, scope string, toolName, argsJSON string) {
	if scope == "" {
		scope = ScopeOnce
	}

	pc.mu.Lock()
	groupName := ""
	if group, routed := pc.routeLocked(toolName); routed {
		groupName = group.Name
	}
	if pc.elevations == nil {
		pc.elevations = make(map[Subject]*elevationState)
	}
	state := pc.elevations[subject]
	if state == nil {
		state = &elevationState{
			groups: make(map[string]bool),
			tools:  make(map[string]bool),
			args:   make(map[string]bool),
		}
		pc.elevations[subject] = state
	}
	switch scope {
	case ScopeSession:
		if groupName != "" {
			state.groups[groupName] = true
		}
	case ScopeTool:
		state.tools[toolName] = true
	case ScopeArgs:
		state.args[toolName+":"+truncateStr(argsJSON, 200)] = true
	case ScopeOnce:
		// 仅本次，不持久化。
	}
	auditor := pc.auditor
	pc.mu.Unlock()

	if auditor != nil {
		auditor(ElevationEvent{
			Subject: subject,
			Group:   groupName,
			Tool:    toolName,
			Reason:  "approval granted",
			Scope:   scope,
			Granted: true,
			At:      time.Now(),
		})
	}
}

// decideLocked 返回动作与可见性。审批通过授予的提权覆盖组默认 ask。调用方需持有读锁。
func (pc *PermissionChecker) decideLocked(subject Subject, toolName, argsJSON string) (CheckResult, bool) {
	group, routed := pc.routeLocked(toolName)
	result := ResultAsk
	if routed && group.Default != "" {
		result = toResult(group.Default)
	}
	groupName := ""
	if routed {
		groupName = group.Name
	}

	visible := true
	if len(pc.subjects) > 0 {
		required := uint8(0)
		resource := ""
		if routed {
			required, resource = group.Mode, group.Resource
		}
		if required != 0 && !pc.hasBitsLocked(subject, groupName, required, resource, toolName, argsJSON) {
			visible = false
			result = missingBitResult(pc.missingBit)
		}
	}

	// 已授予的提权（审批通过）覆盖组默认的 ask：同一 scope 内不再重复询问。
	// Rules 之后仍可覆盖，因此显式 deny 规则不会被提权绕过。
	if result == ResultAsk && pc.elevatedLocked(subject, groupName, toolName, argsJSON) {
		visible = true
		result = ResultAllow
	}

	result = pc.applyRulesLocked(subject, toolName, argsJSON, result, group, routed)
	return result, visible
}

// applyRulesLocked 套用 Rules（LMRW，最后、最细，覆盖组默认）。
func (pc *PermissionChecker) applyRulesLocked(subject Subject, toolName, argsJSON string, result CheckResult, group PermissionGroup, routed bool) CheckResult {
	for _, rule := range pc.rules {
		if !matchGlob(rule.ToolName, toolName) {
			continue
		}
		if !pc.ruleBitsOkLocked(subject, rule, group, routed) {
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

// ruleBitsOkLocked 判定带 Bits 的规则是否对该主体生效。无 Bits（nil）或未启用
// Subjects 时视为生效，保持旧语义。
func (pc *PermissionChecker) ruleBitsOkLocked(subject Subject, rule PermissionRule, group PermissionGroup, routed bool) bool {
	if rule.Bits == nil || len(pc.subjects) == 0 {
		return true
	}
	if !routed {
		return false
	}
	grant, ok := pc.subjects[subject].Bits[group.Name]
	if !ok {
		return false
	}
	return grant.Bits&*rule.Bits == *rule.Bits
}

// routeLocked 按工具名把调用路由到最后一个命中的组（LMRW，与 Rules 一致）。
func (pc *PermissionChecker) routeLocked(toolName string) (PermissionGroup, bool) {
	var matched PermissionGroup
	found := false
	for _, group := range pc.groups {
		for _, pattern := range group.Match {
			if matchGlob(pattern, toolName) {
				matched = group
				found = true
				break
			}
		}
	}
	return matched, found
}

// hasBitsLocked 判定主体是否持有组所需位与资源，或已被提权复用。
func (pc *PermissionChecker) hasBitsLocked(subject Subject, groupName string, required uint8, resource, toolName, argsJSON string) bool {
	if grant, ok := pc.subjects[subject].Bits[groupName]; ok {
		if grant.Bits&required == required && resourceMatches(grant.Resources, resource) {
			return true
		}
	}
	return pc.elevatedLocked(subject, groupName, toolName, argsJSON)
}

// elevatedLocked 查询已记录的提权是否覆盖本次调用。harness 注入了自有台账时
// 以它为准。调用方需持有读锁。
func (pc *PermissionChecker) elevatedLocked(subject Subject, groupName, toolName, argsJSON string) bool {
	if pc.elevationSource != nil {
		return pc.elevationSource.Elevated(subject, toolName, argsJSON)
	}
	state := pc.elevations[subject]
	if state == nil {
		return false
	}
	if groupName != "" && state.groups[groupName] {
		return true
	}
	if state.tools[toolName] {
		return true
	}
	return state.args[toolName+":"+truncateStr(argsJSON, 200)]
}

func resourceMatches(resources []string, resource string) bool {
	if len(resources) == 0 || resource == "" {
		return true
	}
	for _, item := range resources {
		if item == resource {
			return true
		}
	}
	return false
}

// missingBitResult 把 MissingBit 映射为结果；零值（空）等价于 ask。
func missingBitResult(action Action) CheckResult {
	switch action {
	case ActionAllow:
		return ResultAllow
	case ActionDeny:
		return ResultDeny
	default:
		return ResultAsk
	}
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

// NewChannelApprovalHandler 把审批请求投递到 reqCh，并等待 harness 把回复写入
// ApprovalContext.Response。请求自带的 Timeout（<=0 时取 DefaultApprovalTimeout）
// 与调用 ctx 的取消都会终止等待，两者都视作拒绝（不产生任何提权）。
func NewChannelApprovalHandler(reqCh chan<- ApprovalRequest) ApprovalHandler {
	return func(actx *ApprovalContext) (*ApprovalResponse, error) {
		req := actx.Request
		timeout := req.Timeout
		if timeout <= 0 {
			timeout = DefaultApprovalTimeout
		}
		respCh := make(chan *ApprovalResponse, 1)
		// 先挂上回执通道再投递请求：harness 收到请求的那一刻即可安全写入回执
		// （写入 Response 的 happened-before 关系由投递/接收该 channel 建立）。
		actx.Response = respCh
		select {
		case reqCh <- req:
		default:
			return nil, fmt.Errorf("approval: channel full, request dropped")
		}

		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case resp := <-respCh:
			if resp == nil {
				return nil, fmt.Errorf("approval: cancelled")
			}
			resp.Timestamp = time.Now()
			return resp, nil
		case <-timer.C:
			return nil, fmt.Errorf("approval: timeout after %v", timeout)
		case <-contextDone(actx.Context):
			return nil, fmt.Errorf("approval: cancelled")
		}
	}
}

// contextDone 返回 ctx 的 Done 通道；ctx 为 nil 时返回 nil（永不就绪）。
func contextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// matchGlob 匹配工具名 / 参数 glob（仅 '*' 通配）。实现为不分配的贪心通配
// 匹配，避免 strings.Split 在每次判定时产生切片与子串分配。
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
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && pattern[pi] == name[ni]:
			pi++
			ni++
		case pi < len(pattern) && pattern[pi] == '*':
			star = pi
			mark = ni
			pi++
		case star != -1:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// DecideForMeta 是 decideLocked 的「簇属优先」孪生：工具自带的 ToolMeta
// （Groups/Kind/Bits）优先于按名字路由。工具未声明簇属（Groups 与 Bits 均为空）
// 时退回名字路由，行为与旧路径完全一致。
//
// 语义：
//   - meta.Bits 是工具自带的所需位；为 0 时采用候选路由组的 Mode。
//   - meta.Groups 是候选簇；主体在任一候选簇上持有所需位即视为可见。
//   - 缺位 ⇒ visible=false，动作取 MissingBit（默认 ask，与名字路由一致）。
//   - 命中组默认 ask 时，已记录的提权（审批通过）可覆盖为 allow。
//   - Rules 永远最后、最细，覆盖以上一切。
func (pc *PermissionChecker) DecideForMeta(subject Subject, toolName string, meta roottools.ToolMeta, argsJSON string) (CheckResult, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pc.mode == ModeFullAccess {
		return ResultAllow, true
	}
	if pc.allowCache[toolName+":"+truncateStr(argsJSON, 200)] {
		return ResultAllow, true
	}
	if len(meta.Groups) == 0 && meta.Bits == 0 {
		return pc.decideLocked(subject, toolName, argsJSON)
	}
	return pc.decideMetaLocked(subject, toolName, meta, argsJSON)
}

// VisibleForMeta 只取 DecideForMeta 的可见性分量（断位 ⇒ 不在 PATH ⇒ false）。
func (pc *PermissionChecker) VisibleForMeta(subject Subject, toolName string, meta roottools.ToolMeta) bool {
	_, visible := pc.DecideForMeta(subject, toolName, meta, "")
	return visible
}

func (pc *PermissionChecker) decideMetaLocked(subject Subject, toolName string, meta roottools.ToolMeta, argsJSON string) (CheckResult, bool) {
	required := meta.Bits
	clusters := append([]string(nil), meta.Groups...)
	if len(clusters) == 0 {
		if group, ok := pc.routeLocked(toolName); ok {
			clusters = []string{group.Name}
		}
	}

	result := ResultAllow
	groupName := ""
	routed := false
	policy := PermissionGroup{}
	if len(clusters) > 0 {
		groupName = clusters[0]
		if group, ok := pc.groupByNameLocked(groupName); ok {
			policy, routed = group, true
			if required == 0 {
				required = group.Mode
			}
			if group.Default != "" {
				result = toResult(group.Default)
			}
		}
	}

	visible := true
	if len(pc.subjects) > 0 && required != 0 &&
		!pc.metaBitsOkLocked(subject, toolName, required, clusters, meta.Resource, argsJSON) {
		visible = false
		result = missingBitResult(pc.missingBit)
	}
	if result == ResultAsk && pc.elevatedLocked(subject, groupName, toolName, argsJSON) {
		visible = true
		result = ResultAllow
	}

	result = pc.applyRulesLocked(subject, toolName, argsJSON, result, policy, routed)
	return result, visible
}

// metaBitsOkLocked 判定主体是否持有工具自带簇属所需的位：逐个候选簇查授权位，
// 再查提权记录。候选簇为空（工具未声明簇属且名字未路由到任何组）时返回 false。
func (pc *PermissionChecker) metaBitsOkLocked(subject Subject, toolName string, required uint8, clusters []string, resource, argsJSON string) bool {
	for _, cluster := range clusters {
		if grant, ok := pc.subjects[subject].Bits[cluster]; ok &&
			grant.Bits&required == required &&
			resourceMatches(grant.Resources, resource) {
			return true
		}
		if pc.elevatedLocked(subject, cluster, toolName, argsJSON) {
			return true
		}
	}
	return false
}

func (pc *PermissionChecker) groupByNameLocked(name string) (PermissionGroup, bool) {
	for _, group := range pc.groups {
		if group.Name == name {
			return group, true
		}
	}
	return PermissionGroup{}, false
}
