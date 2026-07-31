# 测试报告

## 概览

| 范围 | 通过 | 失败 | 跳过 | 耗时 | 覆盖率 |
| --- | ---: | ---: | ---: | ---: | --- |
| 全仓库 55 个 Go package | 55 | 0 | 0 | 91.2s | 未合并统计 |
| 新增示例 10 个测试 | 10 | 0 | 0 | <1s | 84.6% / 84.6% / 81.4% |

## 各维度

| 维度 | 结果 | 关键指标 |
| --- | :---: | --- |
| 单元测试 | ✅ | 三个新增示例共 10 个测试全部通过 |
| 集成测试 | ✅ | `go test ./... -count=1 -timeout 300s` 覆盖 55 个 package |
| 边界测试 | ✅ | 覆盖未知工具、nil content、非法 Node 实现、缺失 payload 和非法 edge target |
| 可执行冒烟 | ✅ | 三个新增示例分别完成 `go run`，输出与各自 README 一致 |
| 静态分析 | ✅ | `go vet ./...`、`go build ./...`、`git diff --check` 均通过 |
| 架构边界 | ✅ | CI 同款反向依赖与 legacy package 检查通过 |
| 安全检查 | ✅ | 新增示例未包含密钥、Token、真实地址或命令执行能力 |
| 并发竞态 | ⚠️ | 本机缺少 `gcc`，Windows Go 无法启用 CGO，因此本地 `-race` 未执行；CI 的 Ubuntu 分片会执行 race |
| 性能/模糊/泄漏 | — | 本次只增加文档和离线示例，未修改运行时热路径 |

## 覆盖率

| Package | Statements |
| --- | ---: |
| `example_Implement/08_composable_agent` | 84.6% |
| `example_Implement/09_context_pipeline` | 84.6% |
| `example_Implement/10_workplan_codec` | 81.4% |

## 失败详情

没有测试失败。本地 race 命令因环境缺少 C 编译器而未启动：`cgo: C compiler "gcc" not found`；该结果不代表 race 通过或失败。

## 综合判断

- [ ] ✅ 通过
- [x] ⚠️ 有条件通过 — 功能、构建、静态分析和覆盖率均通过；合并前由仓库 CI 完成 Linux race 分片。
- [ ] 🚨 不通过
