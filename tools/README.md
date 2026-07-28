# tools

这是仅在 `tools` 构建标签启用的开发辅助 package，用于将 `goleak` 纳入工具依赖；它不属于运行时框架。

## 实现细节

- 文件包含 `//go:build tools`，普通 `go test ./...` 不会构建该包。
- 以空白导入固定开发工具版本，便于 `go mod` 记录依赖。

## 验证

- 构建：`go list -tags tools ./tools`
