package session

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

type sessionRuntime struct {
	recordingRuntime
	llm types.ChatCompleter
}

func (r sessionRuntime) LLM() types.ChatCompleter { return r.llm }

func TestNewSessionRequiresRuntimeAndLLM(t *testing.T) {
	if _, err := NewSession(SessionComponents{}); err == nil || !strings.Contains(err.Error(), "agent is required") {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := NewSession(SessionComponents{Agent: sessionRuntime{}}); err == nil || !strings.Contains(err.Error(), "agent LLM is required") {
		t.Fatalf("NewSession() error = %v", err)
	}
}

func TestNewSessionAssemblesExplicitComponents(t *testing.T) {
	durable := seelectx.NewMemoryHistory(engineMessage("assistant", "durable checkpoint"))
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	tracer := telemetry.NewMemoryTracer()
	hook, err := telemetry.NewLifecycleHook(tracer, telemetry.WithStrictHookErrors())
	if err != nil {
		t.Fatalf("NewLifecycleHook() error = %v", err)
	}
	session, err := NewSession(SessionComponents{
		Agent:   sessionRuntime{llm: llm},
		History: durable,
		Context: ContextComponents{
			SystemPrompt: "system instructions",
			PromptBlocks: []seelectx.PromptBlock{{
				Name:     "skill",
				Messages: []types.Message{engineMessage("system", "skill instructions")},
			}},
		},
		Telemetry: hook,
		SessionID: "session-components",
		Config:    SessionConfig{MaxLoops: 3},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if session.AgentRuntime() == nil || session.SessionID() != "session-components" {
		t.Fatalf("session agent/id = %#v, %q", session.AgentRuntime(), session.SessionID())
	}

	reply, err := session.Chat(context.Background(), "continue")
	if err != nil || reply != "done" {
		t.Fatalf("Chat() = %q, %v", reply, err)
	}
	if len(llm.seen) != 1 || len(llm.seen[0]) != 4 {
		t.Fatalf("assembled messages = %#v", llm.seen)
	}
	for index, want := range []string{"system instructions", "skill instructions", "durable checkpoint", "continue"} {
		if got := *llm.seen[0][index].Content; got != want {
			t.Fatalf("message[%d] = %q, want %q", index, got, want)
		}
	}
	saved, err := durable.Load(context.Background())
	if err != nil || len(saved) != 3 {
		t.Fatalf("durable history = %#v, %v", saved, err)
	}
	view, err := tracer.Query(context.Background(), telemetry.Query{})
	if err != nil || len(view.Events) == 0 {
		t.Fatalf("telemetry = %#v, %v", view.Events, err)
	}
}

func TestSessionSetSystemPromptReplacesAssemblyBlock(t *testing.T) {
	llm := &recordingLLM{responses: []types.Message{
		engineMessage("assistant", "first"),
		engineMessage("assistant", "second"),
	}}
	session, err := NewSession(SessionComponents{
		Agent:   sessionRuntime{llm: llm},
		Context: ContextComponents{SystemPrompt: "original system"},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := session.Chat(context.Background(), "first turn"); err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	session.SetSystemPrompt("replacement system")
	if _, err := session.Chat(context.Background(), "second turn"); err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
	if len(llm.seen) != 2 || len(llm.seen[1]) == 0 || llm.seen[1][0].Content == nil {
		t.Fatalf("second assembled request = %#v", llm.seen)
	}
	if got := *llm.seen[1][0].Content; got != "replacement system" {
		t.Fatalf("system prompt = %q, want replacement", got)
	}
	for _, message := range llm.seen[1][1:] {
		if message.Role == "system" && message.Content != nil && *message.Content == "original system" {
			t.Fatalf("stale system prompt remained in request: %#v", llm.seen[1])
		}
	}
}

func TestSessionResetClearsInjectedDurableHistory(t *testing.T) {
	durable := seelectx.NewMemoryHistory(engineMessage("assistant", "old response"))
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	session, err := NewSession(SessionComponents{
		Agent:   sessionRuntime{llm: llm},
		History: durable,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if err := session.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if got := session.History(); len(got) != 0 {
		t.Fatalf("working history after Reset = %#v", got)
	}
	stored, err := durable.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("durable history after Reset = %#v", stored)
	}
}
