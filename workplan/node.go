package workplan

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// =============================================================================
// NodeKind
// =============================================================================

type NodeKind int

const (
	kindMethod   NodeKind = iota // Go 函数节点（策略模式）
	kindLLM                      // 纯 LLM 节点（策略模式）
	kindStrategy                 // 自定义策略节点
	kindAuto                     // Agent ReAct 循环，自主执行
	kindApprove                  // 阻塞等人确认
	kindIf                       // 二选一条件分支
	kindSwitch                   // 多路条件分支
	kindLoop                     // 带 Signal 的循环
	kindFork                     // 多 Agent 并发，需 Join 汇合
	kindJoin                     // 汇合 Fork 的结果
	kindCheckpoint               // 快照节点，支持回滚
	kindEmit                     // 把当前结果写入命名变量
)

// =============================================================================
// [workplangate] ExecState — WorkPlan 执行状态机
// =============================================================================

// ExecState 表示 WorkPlan 执行的当前阶段。
// 状态转换：NotStarted → Executing → AwaitingApproval → Executing → Completed/Aborted/Failed
type ExecState int

const (
	StateNotStarted       ExecState = iota // 未执行
	StateExecuting                         // 执行中
	StateAwaitingApproval                  // 暂停，等待人工审批
	StateCompleted                         // 所有节点正常结束
	StateAborted                           // 用户终止
	StateFailed                            // 执行出错
)

func (s ExecState) String() string {
	switch s {
	case StateNotStarted:
		return "not_started"
	case StateExecuting:
		return "executing"
	case StateAwaitingApproval:
		return "awaiting_approval"
	case StateCompleted:
		return "completed"
	case StateAborted:
		return "aborted"
	case StateFailed:
		return "failed"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// =============================================================================
// [workplangate] ChoiceOption / Question — Q-K-V 审批模型
// =============================================================================

// ChoiceOption 表示用户可见的选项（K 部分）。
// Key 是程序匹配用的唯一键，Label/Description 是展示文本。
type ChoiceOption struct {
	Key         string `json:"key"`         // 唯一键，用户回传时用
	Label       string `json:"label"`       // 展示文字
	Description string `json:"description"` // 选项说明
	Style       string `json:"style"`       // 前端样式提示："primary"|"secondary"|"danger"|"warning"|"default"
}

// Question 表示一次完整的人工审批请求（Q + K，V 隐藏）。
// ID 关联两段式调用的前后请求。
// KVS 是服务端持有的键值映射表，不序列化发给客户端。
type Question struct {
	ID      string         `json:"id"`      // 唯一问题 ID（对应 executionID_nodeID）
	Content string         `json:"content"` // 问题/计划内容（Q）
	Options []ChoiceOption `json:"options"` // 可选 K 列表（发给用户）
	KVS     map[string]any `json:"-"`       // K→V 映射表，不序列化，不暴露给 CLI
	Timeout time.Duration  `json:"-"`       // 超时时间，0 表示不限
}

// DefaultChoice 返回第一个选项的 Key 作为默认/超时选择。
func (q Question) DefaultChoice() string {
	if len(q.Options) > 0 {
		return q.Options[0].Key
	}
	return ""
}

// Resolve 根据 choice key 查找对应的 V。找不到时返回默认值。
func (q Question) Resolve(choice string) (any, bool) {
	if v, ok := q.KVS[choice]; ok {
		return v, true
	}
	if key := q.DefaultChoice(); key != "" {
		return q.KVS[key], false
	}
	return nil, false
}

// =============================================================================
// [workplangate] pauseSnapshot — approve 节点暂停点
// =============================================================================

// pauseSnapshot 保存 WorkPlan 在 approve 节点暂停时的执行上下文，
// 供 Resume 时从断点继续。
type pauseSnapshot struct {
	currentID  string            // 暂停时所在的节点 ID
	prevJSON   string            // 上一节点的 JSON 输出
	result     *WorkPlanResult   // 已执行的节点结果（含 Vars 等）
	planText   string            // Plan Agent 生成的结构化计划文本
	question   Question          // 发送给用户的审批问题
	startedAt  time.Time         // WorkPlan 整体开始时间
}

// =============================================================================
// [workplangate] ApproveChoice — 审批结果动作常量
// =============================================================================

// ApproveChoice 是 V 的核心动作类型，switch 匹配用。
type ApproveChoice string

const (
	ChoiceExecute ApproveChoice = "execute" // 执行
	ChoiceSkip    ApproveChoice = "skip"    // 跳过
	ChoiceAbort   ApproveChoice = "abort"   // 终止
)

func (k NodeKind) String() string {
	names := map[NodeKind]string{
		kindMethod:     "Method",
		kindLLM:        "LLM",
		kindStrategy:   "Strategy",
		kindAuto:       "Auto",
		kindApprove:    "Approve",
		kindIf:         "If",
		kindSwitch:     "Switch",
		kindLoop:       "Loop",
		kindFork:       "Fork",
		kindJoin:       "Join",
		kindCheckpoint: "Checkpoint",
		kindEmit:       "Emit",
	}
	if s, ok := names[k]; ok {
		return s
	}
	return "Unknown"
}

// =============================================================================
// Signal —— Loop 对外暴露的活引用
// =============================================================================
//
// Signal 在 Loop 每次迭代结束后更新，外部可以：
//   - Get()      随时读取当前值（无阻塞）
//   - OnUpdate() 注册回调，每次迭代产生新值时立刻触发
//   - Wait()     阻塞直到 Loop 结束，返回最终值
//
// 内部存储 JSON 字符串，方便 LLM 输出直接存储和解析。
// 如果 LLM 输出的是纯文本，就存为 JSON string（带引号的合法 JSON）。

type Signal struct {
	mu        sync.RWMutex
	value     string         // 始终是合法 JSON 字符串
	iter      int            // 当前迭代次数
	cbs       []func(string) // OnUpdate 回调列表
	done      chan struct{}  // Loop 结束时 close
	closeOnce sync.Once
}

func newSignal() *Signal {
	return &Signal{
		done:  make(chan struct{}),
		value: `""`, // 初始值：空 JSON 字符串
	}
}

// set 由 Loop 执行引擎在每次迭代后调用，外部不可直接调用。
// 输入会自动规范化为合法 JSON：
//   - 如果已经是合法 JSON（含 `{...}` `[...]` `"..."` 数字 布尔）→ 直接存储
//   - 否则当作纯文本，包裹为 JSON string
func (s *Signal) set(raw string, iter int) {
	normalized := toJSON(raw)

	s.mu.Lock()
	s.value = normalized
	s.iter = iter
	cbs := make([]func(string), len(s.cbs))
	copy(cbs, s.cbs)
	s.mu.Unlock()

	for _, cb := range cbs {
		cb(normalized)
	}
}

// close 由 Loop 结束时调用，广播 Wait() 解除阻塞。
func (s *Signal) close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// Get 返回当前值的 JSON 字符串（无阻塞）。
// Loop 还未产生任何值时返回 `""`（空 JSON 字符串）。
func (s *Signal) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

// GetString 如果当前值是 JSON string，返回其内容（去掉引号）。
// 如果是 JSON object/array，返回原始 JSON 文本。
func (s *Signal) GetString() string {
	s.mu.RLock()
	raw := s.value
	s.mu.RUnlock()

	var str string
	if json.Unmarshal([]byte(raw), &str) == nil {
		return str
	}
	return raw
}

// Iter 返回当前迭代次数。
func (s *Signal) Iter() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.iter
}

// OnUpdate 注册回调，Loop 每次迭代产生新值时立刻触发（在 set 的 goroutine 里调用）。
// 可注册多个，按注册顺序同步调用。
// Loop 已结束后注册的回调不会被触发。
func (s *Signal) OnUpdate(cb func(jsonValue string)) {
	s.mu.Lock()
	s.cbs = append(s.cbs, cb)
	s.mu.Unlock()
}

// Wait 阻塞直到 Loop 结束（正常结束或 ctx 取消），返回最终值的 JSON 字符串。
func (s *Signal) Wait() string {
	<-s.done
	return s.Get()
}

// =============================================================================
// SwitchCase —— Switch 节点的单个分支
// =============================================================================

type SwitchCase struct {
	// Match 为 nil 时是 default 分支
	Match  func(result string) bool
	NextID string
}

// =============================================================================
// ForkBranch —— Fork 节点的单个并发分支
// =============================================================================

type ForkBranch struct {
	Label        string // 分支标签，用于日志和 Join 结果汇总
	SystemPrompt string // 子 Agent 系统提示词，空时继承 WorkPlan 默认值
	Input        string // 支持 {{.PrevResult}} 和 {{.Vars.key}}
	// EntryNodeID 如果非空，此分支运行一个子 WorkPlan（从该节点开始）
	// 目前预留，后续实现嵌套 WorkPlan
	EntryNodeID string
}

// =============================================================================
// node —— 内部节点结构（私有，外部通过糖函数构建）
// =============================================================================

type node struct {
	id   string
	kind NodeKind

	// ── 执行配置 ──────────────────────────────────────────────────
	systemPrompt string        // 覆盖 WorkPlan 默认 prompt
	input        string        // 输入模板
	toolFilter   []string      // 工具白名单，空表示不限制
	next         string        // 默认下一节点 ID
	strategy     NodeStrategy  // 策略模式：Method/LLM/Agent/自定义策略节点

	// ── kindApprove ───────────────────────────────────────────────
	// [workplangate] 从 []string 改为 []ChoiceOption，支持 K-V 模型
	approveOptions []ChoiceOption // 展示给用户的选项（K 列表）
	approveKVS     map[string]any // K→V 映射表，nil 时自动以 key 为 value
	approveTimeout time.Duration  // 审批超时，0 表示不限

	// ── kindIf ────────────────────────────────────────────────────
	ifCond    func(string) bool // 条件函数
	ifTrueID  string            // 条件为真时跳转
	ifFalseID string            // 条件为假时跳转

	// ── kindSwitch ────────────────────────────────────────────────
	switchCases []SwitchCase // 按顺序匹配，第一个命中的执行

	// ── kindLoop ──────────────────────────────────────────────────
	loopBodyID      string            // 循环体入口节点 ID
	loopUntil       func(string) bool // 返回 true 时退出循环
	loopMaxIter     int               // 最大迭代次数，0 表示不限
	loopSignal      *Signal           // 活引用，每次迭代后更新
	loopExhaustedID string            // 超出 maxIter 后跳转节点 ID

	// ── kindFork ──────────────────────────────────────────────────
	forkBranches []ForkBranch
	joinID       string // 对应的 Join 节点 ID

	// ── kindJoin ──────────────────────────────────────────────────
	// join 节点收集 Fork 的所有分支结果，results 由执行引擎填入
	joinResults []string // 运行时填充，不在构建期使用

	// ── kindEmit ──────────────────────────────────────────────────
	emitKey string // 写入 WorkPlan.vars 的 key

	// ── 流式输出 ─────────────────────────────────────────────────
	onChunk func(string) // 节点执行时的流式输出回调
	// ── 运行时状态 ────────────────────────────────────────────────
	checkpoint *checkpointState // kindCheckpoint 时填充
}

// checkpointState 快照内容。
type checkpointState struct {
	savedAt  time.Time
	snapshot string // 该节点执行完后的输出快照（JSON）
}

// =============================================================================
// NodeResult / WorkPlanResult
// =============================================================================

// NodeResult 记录单个节点的执行情况。
type NodeResult struct {
	NodeID    string
	Kind      string
	Output    string // JSON 字符串
	Skipped   bool
	Aborted   bool
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

// WorkPlanResult 整个 WorkPlan 的执行摘要。
type WorkPlanResult struct {
	NodeResults  []*NodeResult
	Vars         map[string]string // Emit 写入的命名变量（JSON 字符串）
	Checkpoints  map[string]string // nodeID → 快照 JSON
	Aborted      bool
	AbortReason  string
	TotalElapsed time.Duration

	// [workplangate] 两段式协议：暂停时携带 WorkPlan 引用供 Resume
	PausedWorkPlan *WorkPlan `json:"-"` // 内部引用，不序列化
}

// FinalOutput 返回最后一个成功节点的输出（JSON 字符串）。
func (r *WorkPlanResult) FinalOutput() string {
	for i := len(r.NodeResults) - 1; i >= 0; i-- {
		nr := r.NodeResults[i]
		if !nr.Skipped && !nr.Aborted && nr.Err == nil && nr.Output != "" {
			return nr.Output
		}
	}
	return `""`
}

// FinalOutputString 返回最终输出的纯文本（如果是 JSON string 则去引号）。
func (r *WorkPlanResult) FinalOutputString() string {
	raw := r.FinalOutput()
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s
	}
	return raw
}

// =============================================================================
// [workplangate] node 辅助方法
// =============================================================================

// buildKVS 构建 K→V 映射表。若 node.approveKVS 已设置则直接返回，
// 否则自动生成：每个 option key 映射到自身（V == K）。
func (n *node) buildKVS() map[string]any {
	if n.approveKVS != nil {
		return n.approveKVS
	}
	kvs := make(map[string]any, len(n.approveOptions))
	for _, opt := range n.approveOptions {
		kvs[opt.Key] = opt.Key
	}
	return kvs
}

// approvePlanPrompt 生成结构化计划 prompt，引导 Plan Agent 输出 JSON。
func (n *node) approvePlanPrompt(input string) string {
	return fmt.Sprintf(
		`{"action":"plan","task":%q,"instruction":"Analyze the task and output a step-by-step execution plan. Do NOT call any tools. Output ONLY valid JSON, no markdown wrapping.","output_schema":{"summary":"string: one-line plan summary","steps":[{"order":"int","description":"string","tool":"string","actions":"string"}],"expected_output":"string: what this plan will produce"}}`,
		input,
	)
}

// =============================================================================
// 内部工具函数
// =============================================================================

// toJSON 将任意字符串规范化为合法 JSON。
// 已经是合法 JSON 则直接返回；否则包裹为 JSON string。
func toJSON(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// fromJSON 尝试把 JSON string 解包为纯文本；
// 如果是 object/array/number/bool，返回原始 JSON 文本。
func fromJSON(s string) string {
	var str string
	if json.Unmarshal([]byte(s), &str) == nil {
		return str
	}
	return s
}
