# builtin

`builtin` 提供一组由调用方选择注册的产品无关工具；它不读取工作区，不执行 Shell、Git 或文件操作，也不依赖 `agent`。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `New` | 创建包含 `get_time`、`calculate`、`text_stats` 的可选 Provider |
| `Option`、`WithClock` | 注入时间源，不依赖全局时钟状态 |
| `Clock`、`ClockFunc` | 定义 `get_time` 使用的最小时间接口 |
| `ArgumentError` | 返回工具名、JSONPath、行列和具体原因 |

## 实现细节

- `get_time` 默认返回 UTC 的 RFC3339 时间与 Unix 秒，也允许调用方传入 IANA 时区；测试可通过 `WithClock` 注入确定时间。
- `calculate` 只接受加、减、乘、除和两个数值操作数，不执行表达式、脚本或动态代码；除零和非有限结果会返回字段级错误。
- `text_stats` 使用 UTF-8 规则分别统计字节、Unicode code point、空白分词和行数，不读取外部文本来源。
- 所有参数使用严格 JSON 解码，未知字段、类型错误、语法错误和缺失字段都返回 `ArgumentError`，不会通过 panic 报告用户输入问题。
- Provider 构造不会自动注册。调用方需要把 `New()` 的结果显式交给根 `Registry` 或 [`holder`](../holder/README.md)。

## 依赖与验证

- 根工具合约：[`../README.md`](../README.md)
- 命令行真实 API 验证：[`../../cmd/smoke/README.md`](../../cmd/smoke/README.md)
- 验证：`go test ./tools/builtin -count=1`
