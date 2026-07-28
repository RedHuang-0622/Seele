# cmd/repl/prompts

该目录存放 REPL 各插件模式的系统提示词。文件名对应插件名，例如 `read.md` 对应只读检索模式。

## 实现细节

- REPL 启动时加载这些 Markdown，插件切换时同步选择同名提示词和工具过滤规则。
- `default.md` 是未匹配专用模式时的回退提示词；新增模式应同时新增 prompt 文件和 `pluginDef`。

## 验证

- 运行 `go run ./cmd/repl -c <accounts.yaml>`，使用 `/prompts` 与 `/plugin <name>` 检查加载结果。
