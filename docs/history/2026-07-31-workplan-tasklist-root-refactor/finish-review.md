# 最终审查报告

## 变更概览

| 状态 | 文件规模 | 新增 | 删除 | 主要设计模式 |
| --- | ---: | ---: | ---: | --- |
| 未提交工作树 | 143 个 tracked 文件有 diff，另有新目录/文件 | 约 2496 行 | 约 8678 行 | Strategy、Adapter、Provider、Lease、Observer、Dependency Injection |

本轮将 Graph/Plan 双状态、Agent 内工具实现和自动上下文策略拆开，并补充可选 builtin 与真实 API 冒烟入口。根 README 和架构入口已同步到当前边界。

## 审查结论

| 维度 | 状态 | 评分 | 备注 |
| --- | :---: | :---: | --- |
| 正确性 | ⚠️ | B | 全仓测试通过；真实 API 因无凭证未实际调用 |
| 可读性 | ✅ | A | builtin、smoke、根模块和历史记录均有就近文档；错误包含路径与原因 |
| 架构 | ✅ | A | Tools 与 Agent 平行，Plan 单状态源，Context/Telemetry 可选装配，依赖检查通过 |
| 安全性 | ✅ | A | 无真实密钥，无 workspace/Shell builtin；严格 JSON 解码和字段级校验 |
| 性能 | ✅ | B | builtin 为 O(n) 文本统计或 O(1) 计算；P2C/租约路径已有并发测试，但无性能基线 |
| Go 专项 | ⚠️ | B | `go vet`、`go build` 通过；`-race` 被本机 CGO 环境阻塞 |

## 发现的问题

### 🚨 严重（0 个）

未发现循环依赖、真实密钥、命令注入、双状态写入或会造成数据丢失的阻塞问题。

### ⚠️ 警告（3 个）

1. 真实 API 冒烟尚未执行。`cmd/smoke` 和 `TestRealAPIBuiltinSmoke` 已实现并验证 skip 语义，但当前环境没有私有账号配置。
2. Race 检查尚未形成本地结论。Go 工具链报告需要 CGO；Develop CI 已改为在 Ubuntu 上按 6 个 shard 并行执行 `-race`，但需首次 workflow 运行确认。
3. `cmd/smoke` 覆盖率为 73.2%，低于标准测试建议的 80%；主要未覆盖 `main` 的真实 API 成功和退出路径。

### 💡 建议（2 个）

1. 在 CI 增加带密钥保护的手动/定时 real-smoke job，并把账号配置作为 secret 挂载，不写入仓库。
2. 为 `accountpool` P2C 选择、Holder dispatch 和 WorkPlan scheduler 增加稳定 benchmark；为 formal JSON decoder 增加 fuzz target。

## Go 专项记录

- `return nil, nil` 扫描命中 28 处，均位于旧兼容/解析分支或测试；新增 `tools/builtin` 与 `cmd/smoke` 没有该模式。
- 新增 `accountpool`、`telemetry`、`tools/builtin`、`cmd/smoke` 无包级可变状态。
- `go vet ./...` 和 `go build ./...` 零告警。
- 测试中的 `sk-*` 和 `test-key` 均为明确的假凭证；生产代码未硬编码密钥。
- `tools` 依赖列表未出现 `agent`、`workplan`、`seelectx` 或 `accountpool`。

## 亮点

- `builtin.ArgumentError` 同时保留 `errors.As` 能力、JSONPath、行列和人类可读原因。
- `cmd/smoke` 使用 Hook 验证工具的实际 intent/effect，不以“模型回复非空”冒充 Function Calling 成功。
- REPL 显式注册 builtin，保持 Agent 最低构造路径无隐式基础设施。
- WorkPlan formal JSON、邻接表和邻接矩阵最终落到同一 Plan 状态源。
- 根文档不再宣称 round-robin、Graph facade 或 ReAct 自动压缩。

## 最终判断

- [ ] ✅ 通过，可合并
- [x] ⚠️ 有条件通过 — 在具备 CGO 与私有测试凭证的 CI/本地环境补跑 race 和真实 API smoke
- [ ] 🚨 不通过
