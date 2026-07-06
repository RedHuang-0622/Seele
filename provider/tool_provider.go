// Package provider 将所有工具来源（Hub、MCP、Inline）封装为 ToolProvider 接口，
// 供 tool_holder 统一调度。
//
// 类型定义已迁移到 core/tool，此文件仅做 re-export 保持兼容。
package provider

import "github.com/RedHuang-0622/Seele/agent/tool"

// ToolHandler 委托到 core/tool
type ToolHandler = tool.ToolHandler

// ToolEntry 委托到 core/tool
type ToolEntry = tool.ToolEntry

// ToolProvider 委托到 core/tool
type ToolProvider = tool.ToolProvider

// ErrToolUnavailable 委托到 core/tool
var ErrToolUnavailable = tool.ErrToolUnavailable
