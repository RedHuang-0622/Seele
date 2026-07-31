package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

type blockingCompleter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCompleter) Complete(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	content := "done"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func (c *blockingCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(string)) (string, string, []types.ToolCall, error) {
	message, err := c.Complete(ctx, messages, tools)
	if err != nil || message.Content == nil {
		return "", "", nil, err
	}
	return *message.Content, "", nil, nil
}

func (c *blockingCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return c.CompleteStream(ctx, messages, tools, nil)
}

func TestSessionSerializesConcurrentChats(t *testing.T) {
	llm := &blockingCompleter{started: make(chan struct{}), release: make(chan struct{})}
	session, err := NewSession(SessionComponents{Agent: sessionRuntime{llm: llm}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { _, err := session.Chat(context.Background(), "first"); firstDone <- err }()
	<-llm.started
	go func() { _, err := session.Chat(context.Background(), "second"); secondDone <- err }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Chat completed before first turn released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(llm.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
}
