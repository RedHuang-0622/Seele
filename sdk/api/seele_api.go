// Package api 是 Seele SDK 的公共 API 封装层，提供便捷的类型别名。
//
// 底层实现在 core/agent、core/session、core/tool_holder 中。
package api

import (
	"github.com/sukasukasuka123/Seele/core/agent"
	"github.com/sukasukasuka123/Seele/core/session"
)

// ── 类型别名 ──────────────────────────────────────────────────────────

// Options 是 Agent 的启动配置。
type Options = agent.Options

// Engine 是 Seele 的编排器。等同于 agent.Agent。
type Engine = agent.Agent

// Logger 是日志接口。
type Logger = agent.Logger

// New 创建 Engine。等同于 agent.New。
var New = agent.New

// ── Session ───────────────────────────────────────────────────────────

// Session 是一次对话会话的持有者。等同于 session.Holder。
type Session = session.Holder

// Pool 管理多个 Session。
type Pool = agent.Pool

// Summary 是会话摘要。
type Summary = agent.Summary
