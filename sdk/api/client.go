package api

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// AgentClient 是 Seele Agent 的 gRPC 客户端。
// 封装了与远程 Agent 服务的连接和 RPC 调用。
type AgentClient struct {
	conn *grpc.ClientConn
}

// Dial 连接到指定地址的 Agent gRPC 服务。
func Dial(addr string) (*AgentClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &AgentClient{conn: conn}, nil
}

// Close 关闭连接。
func (c *AgentClient) Close() error { return c.conn.Close() }

// Chat 执行一次远程对话。
func (c *AgentClient) Chat(ctx context.Context, sessionID, input string) (string, error) {
	var resp ChatResponse
	err := c.conn.Invoke(ctx, "/seele.api.AgentService/Chat",
		&ChatRequest{SessionID: sessionID, Input: input}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Reply, nil
}

// NewSession 在远程 Agent 上创建一个新会话。
func (c *AgentClient) NewSession(ctx context.Context, systemPrompt string, maxLoops int32) (string, error) {
	var resp NewSessionResponse
	err := c.conn.Invoke(ctx, "/seele.api.AgentService/NewSession",
		&NewSessionRequest{SystemPrompt: systemPrompt, MaxLoops: maxLoops}, &resp)
	if err != nil {
		return "", err
	}
	return resp.SessionID, nil
}

// Health 健康检查。
func (c *AgentClient) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	err := c.conn.Invoke(ctx, "/seele.api.AgentService/Health", nil, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
