# test

`test` 保存跨 package 的集成与烟雾测试，验证用户可见流程，而非替代各模块目录中的单元测试。

## 覆盖范围

- Agent/账户配置、工具切换、ReAct 循环和 tracer 的端到端路径。
- WorkPlan 图、并发池、限流和 probe 等跨模块行为。

## 实现细节

- `helpers_test.go` 提供共享测试构造器；其余测试以真实模块组合测试公开 API。
- 外部服务相关用例应使用可控替身或明确的环境前提，避免把密钥写入源码。

## 验证

- `go test ./test/... -count=1`
