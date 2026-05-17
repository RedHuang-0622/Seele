package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// =============================================================================
// ApprovalGate —— 人工确认的 IO 抽象
// =============================================================================
//
// 设计原则：
//   WorkPlan 执行引擎只调用这一个接口，不感知任何 IO 细节。
//   命令行、WebSocket、HTTP long-poll 只需各自实现 ApprovalGate。
//
// 网页版实现骨架（不在本包实现，由上层注入）：
//
//   type WebApprovalGate struct {
//       pending sync.Map // nodeID → chan ApprovalResponse
//   }
//   func (g *WebApprovalGate) Request(ctx, nodeID, plan, opts) (string, error) {
//       ch := make(chan string, 1)
//       g.pending.Store(nodeID, ch)
//       defer g.pending.Delete(nodeID)
//       select {
//       case choice := <-ch: return choice, nil
//       case <-ctx.Done():   return "", ctx.Err()
//       }
//   }
//   // HTTP handler 收到前端点击后：
//   func (g *WebApprovalGate) Respond(nodeID, choice string) {
//       if ch, ok := g.pending.Load(nodeID); ok {
//           ch.(chan string) <- choice
//       }
//   }

// ApprovalRequest 是展示给人的完整上下文。
// plan 字段是 JSON 字符串，方便网页端直接解析渲染。
type ApprovalRequest struct {
	NodeID  string   `json:"node_id"`
	Plan    string   `json:"plan"`    // Agent 生成的计划文本（也可以是 JSON）
	Options []string `json:"options"` // 可选项列表
}

// ApprovalGate 人工确认接口。
// Request 阻塞直到人做出选择，返回选中的 option 字符串。
type ApprovalGate interface {
	Request(ctx context.Context, req ApprovalRequest) (string, error)
}

// =============================================================================
// CLIApprovalGate —— 命令行阻塞实现
// =============================================================================

type CLIApprovalGate struct{}

func (g *CLIApprovalGate) Request(ctx context.Context, req ApprovalRequest) (string, error) {
	// 尝试把 plan 解析为 JSON 美化输出，失败则直接打印
	planStr := req.Plan
	var planJSON any
	if json.Unmarshal([]byte(req.Plan), &planJSON) == nil {
		if pretty, err := json.MarshalIndent(planJSON, "│  ", "  "); err == nil {
			planStr = string(pretty)
		}
	}

	fmt.Printf("\n┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│  [需要确认] 节点: %-26s│\n", req.NodeID)
	fmt.Printf("├─────────────────────────────────────────────┤\n")
	fmt.Println("│  Agent 计划：")
	for _, line := range strings.Split(planStr, "\n") {
		fmt.Printf("│    %s\n", line)
	}
	fmt.Printf("├─────────────────────────────────────────────┤\n")
	for i, opt := range req.Options {
		fmt.Printf("│  [%d] %s\n", i+1, opt)
	}
	fmt.Printf("└─────────────────────────────────────────────┘\n")
	fmt.Print("请输入选项编号或文本: ")

	inputCh := make(chan string, 1)
	go func() {
		var s string
		fmt.Scanln(&s)
		inputCh <- strings.TrimSpace(s)
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case raw := <-inputCh:
		return resolveChoice(raw, req.Options), nil
	}
}

// resolveChoice 将用户输入（数字或文本）解析为 options 中的选项。
// 无法识别时返回最后一个选项（通常是"终止"）。
func resolveChoice(raw string, options []string) string {
	if raw == "" {
		return options[len(options)-1]
	}
	// 数字索引
	for i, opt := range options {
		if raw == fmt.Sprintf("%d", i+1) {
			return opt
		}
	}
	// 文本匹配（大小写不敏感）
	for _, opt := range options {
		if strings.EqualFold(raw, opt) {
			return opt
		}
	}
	fmt.Printf("[无法识别 %q，视为终止]\n", raw)
	return options[len(options)-1]
}
