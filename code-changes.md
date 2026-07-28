# 代码变更摘要

## 新增/修改/删除文件

| 文件 | 类型 | 说明 | 设计模式 |
| --- | --- | --- | --- |
| `docs/DOCUMENTATION_STANDARD.md` | 新增 | 唯一文档标准：模块分层、落盘规则、模板与检查项 | 单一事实来源 |
| `AGENTS.md` | 新增 | 要求每次 Markdown 变更前读取文档标准 | 约定优于配置 |
| `*/README.md` | 新增 | 为源码模块、子系统、命令、示例和测试提供就地说明 | 模块化文档 |
| `docs/README.md`、`DOCUMENTATION.md` | 修改 | 指向标准和跨模块文档入口 | 导航 |
| `docs/history/` | 新增/迁移 | 统一归档带日期的历史文件与目录 | 文档生命周期管理 |

## API 变更

无。此变更不修改 Go 代码或对外 API。

## 接口抽象

无新增或修改接口。

## 循环依赖检查

- [x] 仅新增和更新 Markdown 文档，未改变 Go import 图。

## 验证

- [x] 已确认 49 个可构建 Go package 均有 `README.md`。
- [x] 已校验新增与更新文档的本地相对链接。
- [!] `go test ./...`：除 `agent/core/tool/builtin.TestBashHandler` 外均通过；该测试中的 bash 调用在 10 秒后超时。此次仅修改文档，未改动该测试或其实现。

## 建议 commit

`docs(repository): establish module documentation standard`
