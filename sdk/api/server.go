// Package api 提供 Seele Agent 的网络 API（gRPC 服务端 + 客户端）。
//
// Server 将 *agent.Agent 暴露为 gRPC 服务，供远程客户端调用：
//
//	a, _ := agent.New(agent.Options{LLMConfig: llmCfg})
//	defer a.Shutdown()
//	api.Serve(a, ":51111")
package api

import (
	"context"
	"net"

	"github.com/RedHuang-0622/Seele/core/agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── 手工 proto 消息类型（无需 protoc 生成） ────────────────────────────

// ChatRequest 对应 agent.proto ChatRequest。
type ChatRequest struct {
	SessionID string `protobuf:"bytes,1,opt,name=session_id" json:"session_id,omitempty"`
	Input     string `protobuf:"bytes,2,opt,name=input" json:"input,omitempty"`
}

// ChatResponse 对应 agent.proto ChatResponse。
type ChatResponse struct {
	Reply string `protobuf:"bytes,1,opt,name=reply" json:"reply,omitempty"`
	Loops int32  `protobuf:"varint,2,opt,name=loops" json:"loops,omitempty"`
}

// NewSessionRequest 对应 agent.proto NewSessionRequest。
type NewSessionRequest struct {
	SystemPrompt string `protobuf:"bytes,1,opt,name=system_prompt" json:"system_prompt,omitempty"`
	MaxLoops     int32  `protobuf:"varint,2,opt,name=max_loops" json:"max_loops,omitempty"`
}

// NewSessionResponse 对应 agent.proto NewSessionResponse。
type NewSessionResponse struct {
	SessionID string `protobuf:"bytes,1,opt,name=session_id" json:"session_id,omitempty"`
}

// HealthResponse 对应 agent.proto HealthResponse。
type HealthResponse struct {
	Status    string `protobuf:"bytes,1,opt,name=status" json:"status,omitempty"`
	ToolCount int32  `protobuf:"varint,2,opt,name=tool_count" json:"tool_count,omitempty"`
}

// ── Server ───────────────────────────────────────────────────────────

// AgentServer 将 *agent.Agent 暴露为 gRPC 服务。
type AgentServer struct {
	agent    *agent.Agent
	sessions map[string]*sessionRef // sessionID → config
}

type sessionRef struct {
	prompt   string
	maxLoops int
}

// NewServer 创建 Agent gRPC 服务实例。
func NewServer(a *agent.Agent) *AgentServer {
	return &AgentServer{agent: a, sessions: make(map[string]*sessionRef)}
}

// Serve 在指定地址启动 gRPC 服务。阻塞直到出错或进程退出。
// 这是 sdk/api 包的唯一对外入口——替代旧 type-alias 的 api.New()。
func Serve(a *agent.Agent, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	registerService(srv, NewServer(a))
	return srv.Serve(lis)
}

// ── Service registration (手工 ServiceDesc，无需 protoc 代码生成) ─────

func registerService(s *grpc.Server, impl *AgentServer) {
	// 仿照 protobuf 生成代码的 grpc.ServiceDesc 结构
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "seele.api.AgentService",
		HandlerType: (*AgentServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Chat",
				Handler:    impl.chatHandler,
			},
			{
				MethodName: "NewSession",
				Handler:    impl.newSessionHandler,
			},
			{
				MethodName: "Health",
				Handler:    impl.healthHandler,
			},
		},
	}, impl)
}

// ── RPC 实现 ─────────────────────────────────────────────────────────

func (s *AgentServer) chatHandler(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
	var req ChatRequest
	if err := dec(&req); err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	ref, ok := s.sessions[req.SessionID]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionID)
	}

	sess := s.agent.NewSession(ref.prompt, ref.maxLoops)
	reply, err := sess.Chat(ctx, req.Input)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "chat failed: %v", err)
	}
	return &ChatResponse{Reply: reply}, nil
}

func (s *AgentServer) newSessionHandler(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
	var req NewSessionRequest
	if err := dec(&req); err != nil {
		return nil, err
	}
	maxLoops := int(req.MaxLoops)
	if maxLoops <= 0 {
		maxLoops = 8
	}
	sess := s.agent.NewSession(req.SystemPrompt, maxLoops)
	sid := sess.SessionID()

	s.sessions[sid] = &sessionRef{prompt: req.SystemPrompt, maxLoops: maxLoops}
	return &NewSessionResponse{SessionID: sid}, nil
}

func (s *AgentServer) healthHandler(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
	return &HealthResponse{Status: "ok", ToolCount: int32(len(s.agent.Tools().Tools()))}, nil
}
