package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/RedHuang-0622/microHub/pb_api"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
)

// HubToolHandler 通过 gRPC 调用远程 microHub Skill 进程。
// 实现 ToolHandler 接口，封装了协议适配和结果解析。
type HubToolHandler struct {
	Hub     *hubbase.BaseHub
	Method  string
	Timeout time.Duration
}

func (h *HubToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	// 构建 gRPC 请求
	req, err := pb_api.Request().
		Method(h.Method).
		Params([]byte(argsJSON)).
		Build()
	if err != nil {
		return "", fmt.Errorf("HubToolHandler: build request for %q: %w", h.Method, err)
	}

	start := time.Now()
	results := h.Hub.Dispatch(ctx, req)

	if len(results) == 0 {
		return "", fmt.Errorf("%w: HubToolHandler: method=%s: no response", ErrToolUnavailable, h.Method)
	}

	var parts, errs []string
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
			continue
		}
		for _, resp := range r.Responses {
			switch resp.Status {
			case "error":
				for _, e := range resp.Errors {
					errs = append(errs, fmt.Sprintf("[%s] %s: %s", resp.ToolName, e.Code, e.Message))
				}
			case "ok", "partial":
				if raw := string(resp.Result); raw != "" && raw != "{}" {
					parts = append(parts, raw)
				}
			}
		}
	}

	if len(errs) > 0 && len(parts) == 0 {
		if allTransportErrors(results) {
			return "", fmt.Errorf("%w: HubToolHandler: method=%s: %s",
				ErrToolUnavailable, h.Method, strings.Join(errs, "; "))
		}
		return "", fmt.Errorf("HubToolHandler: method=%s failed: %s", h.Method, strings.Join(errs, "; "))
	}

	log.Printf("[HubToolHandler] method=%s latency=%dms", h.Method, time.Since(start).Milliseconds())
	return strings.Join(parts, "\n"), nil
}

// allTransportErrors 判断所有 DispatchResult 是否均为传输层错误（工具不可达）。
func allTransportErrors(results []hubbase.DispatchResult) bool {
	if len(results) == 0 {
		return true
	}
	for _, r := range results {
		if r.Err == nil && len(r.Responses) > 0 {
			return false
		}
	}
	return true
}

// init 确保 hub_handler.go 编译期不引入 json 未使用错误
var _ = json.Valid
