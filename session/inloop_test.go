package session

import (
	"context"
	"errors"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// TestInLoopHandleFoldsInsideTheTurn 验证环内把手的三件事：
//  1. 工具 Dispatch 内凭 ctx 拿得到把手，且能读到本轮当前历史（含 in-flight
//     assistant），不需要再抢 Session.mu（不抢 = 不自锁）；
//  2. 丢掉在飞 tool_call 单元的替换被拒（ErrInLoopInFlightDropped）且历史不
//     动；保留尾部的替换当场生效，**同回合**的下一次模型请求就带着折叠结果；
//  3. 回合结束后同一个 ctx 再也取不到把手（世代守卫）。
func TestInLoopHandleFoldsInsideTheTurn(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()
	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("newTestAgent failed: %v", err)
	}
	defer a.Shutdown()

	var (
		handleSeen    bool
		historyAtCall []types.Message
		dropErr       error
		foldErr       error
		staleRoundCtx context.Context
		inFlightTail  []types.Message
	)

	a.RegisterTool("probe_fold", "folds the history from inside the turn", map[string]interface{}{
		"type": "object",
	}, func(ctx context.Context, _ string) (string, error) {
		staleRoundCtx = ctx
		handle, ok := InLoopFrom(ctx)
		handleSeen = ok
		if !ok {
			return "no in-loop handle", nil
		}
		current, err := handle.History()
		if err != nil {
			t.Errorf("in-loop History failed: %v", err)
			return "history error", nil
		}
		historyAtCall = current
		// 在飞尾部 = 最后一条带 tool_calls 的 assistant（其 tool 结果尚未 append）。
		for i := len(current) - 1; i >= 0; i-- {
			if current[i].Role == "assistant" && len(current[i].ToolCalls) > 0 {
				inFlightTail = append([]types.Message(nil), current[i])
				break
			}
		}
		// 先试「丢掉在飞尾部」：必须被拒且不改历史。
		dropped := types.Message{Role: "user", Content: stringPointer("COMPACTED-FRAME")}
		dropErr = handle.ReplaceHistory([]types.Message{dropped})
		if after, _ := handle.History(); len(after) != len(current) {
			t.Errorf("rejected replace still mutated history: before=%d after=%d", len(current), len(after))
		}
		// 再试「保留尾部」：当场生效。
		foldErr = handle.ReplaceHistory(append([]types.Message{dropped}, inFlightTail...))
		return "folded inside the turn", nil
	})

	eng := New(a)
	for i := 0; i < 4; i++ {
		eng.AppendHistory(types.Message{Role: "user", Content: stringPointer("seed-question")})
		eng.AppendHistory(types.Message{Role: "assistant", Content: stringPointer("seed-answer")})
	}
	mockSrv.EnqueueToolCalls([]types.ToolCall{{
		ID: "call-fold-1", Type: "function",
		Function: types.ToolCallFunction{Name: "probe_fold", Arguments: "{}"},
	}})
	mockSrv.EnqueueText("final answer")

	if _, err := eng.Chat(context.Background(), "go"); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if !handleSeen {
		t.Fatal("工具在 Dispatch 内拿不到环内把手（ctx 未携带本轮能力）")
	}
	if len(historyAtCall) != 10 {
		t.Fatalf("环内读到的历史长度 = %d, want 10（4 对种子 + user + assistant tool_calls）", len(historyAtCall))
	}
	if !errors.Is(dropErr, ErrInLoopInFlightDropped) {
		t.Fatalf("丢掉在飞尾部的替换 = %v, want ErrInLoopInFlightDropped", dropErr)
	}
	if foldErr != nil {
		t.Fatalf("保留尾部的替换失败: %v", foldErr)
	}

	final := eng.History()
	// 折叠后循环照常续写：tool 结果 + 第二次模型调用的最终 assistant。
	if len(final) != 4 {
		t.Fatalf("折叠后历史长度 = %d, want 4（帧 + 在飞 assistant + tool 结果 + 最终 assistant）", len(final))
	}
	if contentOf(final[0]) != "COMPACTED-FRAME" {
		t.Fatalf("折叠帧没有落在历史首位: %q", contentOf(final[0]))
	}
	if final[1].Role != "assistant" || len(final[1].ToolCalls) == 0 {
		t.Fatal("在飞 assistant 被丢了")
	}
	if final[2].Role != "tool" || final[2].ToolCallID != "call-fold-1" {
		t.Fatalf("tool 结果成了孤儿: role=%s callID=%s", final[2].Role, final[2].ToolCallID)
	}
	if final[3].Role != "assistant" || contentOf(final[3]) != "final answer" {
		t.Fatalf("折叠后的第二次模型调用没有正常收尾: role=%s content=%q", final[3].Role, contentOf(final[3]))
	}

	seen := mockSrv.Seen()
	if len(seen) != 2 {
		t.Fatalf("模型调用次数 = %d, want 2", len(seen))
	}
	if promptContains(seen[0].Messages, "COMPACTED-FRAME") {
		t.Fatal("第一次请求不该带折叠帧")
	}
	if !promptContains(seen[1].Messages, "COMPACTED-FRAME") {
		t.Fatal("折叠未即时生效：同回合的下一次请求仍看不到折叠帧")
	}
	if promptContains(seen[1].Messages, "seed-answer") {
		t.Fatal("折叠生效但旧轮次仍在请求里")
	}

	if _, ok := InLoopFrom(staleRoundCtx); ok {
		t.Fatal("回合结束后旧 ctx 仍能取到环内把手（世代守卫失效）")
	}
}

// TestInLoopHandleUnavailableOutsideTurn 是把手的反面：环外既取不到把手，也不
// 应该被拿来当「第二条读历史的路」——环外照旧走 Session.History()（取锁，正常
// 排队）。
func TestInLoopHandleUnavailableOutsideTurn(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()
	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("newTestAgent failed: %v", err)
	}
	defer a.Shutdown()

	eng := New(a)
	if _, ok := InLoopFrom(context.Background()); ok {
		t.Fatal("环外不该取到把手")
	}
	if _, ok := InLoopFrom(nil); ok { //nolint:staticcheck // 显式验证 nil ctx 不 panic
		t.Fatal("nil ctx 不该取到把手")
	}
	mockSrv.EnqueueText("plain")
	if _, err := eng.Chat(context.Background(), "go"); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if _, ok := InLoopFrom(context.Background()); ok {
		t.Fatal("回合结束后环外仍不该取到把手")
	}
}

// stringPointer / contentOf / promptContains 是本文件私有的小助手。
func stringPointer(s string) *string { return &s }

func contentOf(msg types.Message) string {
	if msg.Content == nil {
		return ""
	}
	return *msg.Content
}

func promptContains(messages []types.Message, want string) bool {
	for _, msg := range messages {
		if contentOf(msg) == want {
			return true
		}
	}
	return false
}
