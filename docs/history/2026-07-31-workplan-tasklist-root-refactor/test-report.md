# 测试报告

## 概览

| Package 总数 | 通过 | 失败 | 无测试但已编译 | 条件跳过 | 全仓耗时 | 新增核心覆盖率 |
| --- | --- | --- | --- | --- | --- | --- |
| 52 | 52 | 0 | 8 | 1 个真实 API 用例 | 100.9s | `tools/builtin` 89.1% |

条件跳过项是 `TestRealAPIBuiltinSmoke`：当前环境没有 `SMOKE_CONFIG` 或 `SEELE_SMOKE_*` 凭证。测试明确输出 skip，不会把离线 mock 冒充真实 API 结果。

## 各维度

| 维度 | 结果 | 关键指标 |
| --- | :---: | --- |
| 单元测试 | ✅ | `go test ./... -count=1 -timeout 300s` 全部通过 |
| 集成测试 | ✅ | Agent → Engine → builtin tool → tool result → final reply 离线端到端通过 |
| 真实 API 冒烟 | ⚠️ | 命令与 gated test 已完成；缺少外部凭证，实际请求未执行 |
| 边界测试 | ✅ | 非法 JSON、类型、未知字段、缺失字段、除零、非法时区均有语义错误测试 |
| WorkPlan | ✅ | formal EdgeList、邻接表/矩阵、Plan/runtime/sugar 全包通过 |
| AccountPool | ✅ | P2C、租约、HTTP/stream 释放及兼容网关测试通过 |
| 性能测试 | ⚠️ | benchmark 命令通过，但目标包当前没有 benchmark 用例 |
| 并发测试 | ⚠️ | 普通并发测试通过；`-race` 因本机无 CGO 工具链未运行 |
| 模糊测试 | ⚠️ | 本轮未新增 fuzz target |
| 静态分析 | ✅ | `go vet ./...`、`go build ./...`、`git diff --check` 通过 |
| CI 配置 | ✅ | `actionlint` 通过；6 个 shard 的 package pattern 与 coverage profile 命令已验证 |
| 安全检查 | ✅ | 无真实密钥；凭证仅从外部配置/环境读取；builtin 不执行文件、Git 或 Shell |
| 依赖检查 | ✅ | 旧工具/Graph/DSL Go 路径无引用；`tools` 无 `agent/workplan/seelectx/accountpool` 反向依赖 |

## 覆盖率

| Package | 覆盖率 | 说明 |
| --- | ---: | --- |
| `tools/builtin` | 89.1% | 覆盖 Provider、三类工具、严格解码及语义错误 |
| `cmd/smoke` | 73.2% | 配置和离线 suite 已覆盖；`main` 退出路径与真实 API 成功分支未覆盖 |
| `accountpool` | 86.2% | 全仓覆盖率执行时记录 |
| `workplan/runtime/executor` | 100.0% | 全仓覆盖率执行时记录 |
| `workplan/runtime/validate` | 94.2% | 全仓覆盖率执行时记录 |

全仓覆盖率命令已逐 package 成功执行；仓库包含多个 command/example 包，无法用单一百分比反映核心模块质量。新增 builtin 达到标准测试建议的 85% 阈值；`cmd/smoke` 低于 80%，原因是不能在无凭证环境覆盖真实 API 主入口。

## 环境限制

| 命令 | 结果 | 后续条件 |
| --- | --- | --- |
| `go test -race ./... -count=1` | `-race requires cgo; enable cgo by setting CGO_ENABLED=1` | 在安装 GCC/CGO 的 Windows 或 Linux CI 补跑 |
| `go test ./cmd/smoke -run TestRealAPIBuiltinSmoke -v` | 明确 skip | 提供私有账号配置或 `SEELE_SMOKE_*` 环境变量 |

Develop CI 已在 Ubuntu runner 上为全部 6 个测试分片启用 `CGO_ENABLED=1` 和 `-race`；该结论需要 workflow 实际运行后才能从“已配置”升级为“已通过”。

## 综合判断

- [ ] ✅ 通过
- [x] ⚠️ 有条件通过 — 代码、普通测试、静态分析和离线端到端均通过；合并前或 CI 中补跑 race 与真实 API 冒烟
- [ ] 🚨 不通过
