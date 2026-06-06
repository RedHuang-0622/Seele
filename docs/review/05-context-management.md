# 上下文管理

> Token 估算 · LLM 压缩 · 硬截断 · 工具结果裁剪

---

## 1. 问题背景

LLM 的上下文窗口有限（如 8K/32K/128K token）。ReAct 循环中每轮都会追加 assistant(tool_calls) 和 tool_result 到 history，如果不加控制，history 会无限膨胀直至超出模型限制 → API 报错或静默截断。

Seele 的三层防护：

```
第一道防线：工具结果截断（每次 dispatch 后立即执行）
     ↓  超了？仍有风险
第二道防线：LLM 压缩（history token 超 CompressThreshold 时触发）
     ↓  超了？压缩 LLM 调用本身可能失败
第三道防线：硬截断（压缩失败或压缩后仍超限时强制执行）
     ↓  超了？意味着 system prompt 本身就太大
保底：截断 system 消息内容
```

## 2. Token 估算

```go
// 保守公式：len(text) / 3
// 中文 UTF-8 每字 3 字节 ≈ 1-2 token
// 英文每字符 1 字节 ≈ 0.25 token
// 取 /3 对两类都偏保守（高估 token 数）
func EstimateTokens(text string) int {
    if text == "" { return 0 }
    return (len(text) + 2) / 3
}
```

**为什么用字节估算而非真实 tokenizer**：
- 不需要引入 tiktoken 等 CGO 依赖
- `/3` 对中英文都偏保守 → 不会超过 LLM 窗口
- 速度快，每次 dispatch 后都调用

## 3. 压缩算法

### 3.1 触发条件

```go
if h.lastCompressLoop >= 0 && loop - h.lastCompressLoop <= 1 {
    return  // 刚压缩过，跳过一次
}
if EstimateHistoryTokens(h.history) > cfg.CompressThreshold {
    // 触发压缩
}
```

`lastCompressLoop` 防止连续两轮都压缩（压缩本身消耗 token）。

### 3.2 压缩步骤

```
history: [sys, user1, assistant(tool_calls), tool_result1, tool_result2,
          user2, assistant(tool_calls), tool_result3, user3, assistant(text)]

1. splitSystem → sys + rest
2. rest 末尾 keepRecent=4 条完整保留
   前部 → compressible 部分
3. 配对保护：如果 keep 头部是 tool 消息，
   其 assistant(tool_calls) 还在 compressible 末尾 →
   向回扩展 keep 直到配对完整
4. buildCompressInput → 序列化为文本（截断过长 tool 结果到 800 字符）
5. callCompressLLM → 调用 LLM (无 tools, 低温，低 max_tokens)
   → 生成 150 词以内的英文摘要
6. 组装：sys + [压缩摘要 system 消息] + keep
7. 检查：压缩后仍超限 → 硬截断
```

### 3.3 压缩 prompt

```
Summarize the following execution history.
Focus on: key findings, errors encountered, actions taken, and final outcomes.
Be extremely concise — output a short paragraph under 150 words.
Do NOT re-execute any tools. Only summarize what already happened.
```

### 3.4 硬截断

```go
func TrimHistory(msgs []types.Message, maxTokens int) []types.Message {
    // 1. 保留所有 system 消息
    // 2. 从最旧的 user/assistant/tool 开始丢弃
    // 3. 丢弃后清理头部的孤儿 tool 消息
    //    (其 assistant(tool_calls) 已被丢弃，tool 消息单独存在会导致 API 报错)
    // 4. 保底：若仅 system 消息就超限，截断 system 内容
}
```

## 4. 工具结果截断

```go
func TruncateToolResult(content string, maxChars int) string {
    if len(content) <= maxChars { return content }
    cut := content[:maxChars]
    // 尽量在换行符处截断，保持可读性
    if idx := strings.LastIndex(cut, "\n"); idx > maxChars/2 {
        cut = cut[:idx]
    }
    return cut + "\n...[truncated]"
}
```

**为什么加 `[truncated]` 标记**：让 LLM 知道信息被裁剪了，避免它基于不完整数据做出错误推断。这是一个 AI 工程特有的设计考量。

## 5. 配置项

```go
type ContextConfig struct {
    MaxTokens           int  // 硬上限，默认 8192
    CompressThreshold   int  // 压缩阈值，默认 6144 (75%)
    MaxToolResultChars  int  // 工具结果截断长度，默认 4000
}

func DefaultContextConfig() ContextConfig {
    return ContextConfig{
        MaxTokens:          8192,
        CompressThreshold:  6144,
        MaxToolResultChars: 4000,
    }
}
```

**配置建议**：
- 小模型 (4K)：`MaxTokens=4096, CompressThreshold=3072`
- 大模型 (32K)：`MaxTokens=32768, CompressThreshold=24576`
- 工具返回大量数据：调高 `MaxToolResultChars`
- 对话轮次短：调高 `CompressThreshold`（更少压缩，更好的上下文保真）

## 6. 配对保护

历史上最 subtle 的 bug：截断/压缩拆散了 assistant(tool_calls) 和 tool_result 的配对。

```
assistant: {tool_calls: [call_1, call_2]}
tool: result_1
tool: result_2
```

如果硬截断恰好把 assistant 丢弃而保留 tool 消息，LLM API 会报错（孤立的 tool 消息没有对应的 tool_call_id）。

**修复**：`stripLeadingOrphanTools()` 在截断后清理头部的孤儿 tool 消息。

压缩时同理：通过扩展 `keep` 范围确保配对完整。

## 7. 压缩 LLM 调用的安全措施

```go
func callCompressLLM(ctx context.Context, client types.ChatCompleter, input string) (string, error) {
    // 空 tools 列表 → LLM 不能发起 tool_call
    messages := []types.Message{
        {Role: "system", Content: strPtr(compressSystemPrompt)},
        {Role: "user", Content: &input},
    }
    msg, err := client.Complete(ctx, messages, nil)
    // ...
}
```

- `tools: nil` → LLM 无法发起工具调用（防止压缩过程触发新工具）
- 失败时 fallback 到硬截断（不中断对话）
