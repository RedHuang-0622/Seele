package types

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPStatusErrorKeepsLegacyText(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limited"}}`)
	error := NewHTTPStatusError(http.StatusTooManyRequests, body, nil)

	// 历史格式：fmt.Errorf("HTTP %d: %.512s", status, body)
	want := fmt.Sprintf("HTTP %d: %.512s", http.StatusTooManyRequests, body)
	if got := error.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestHTTPStatusErrorTruncatesLongBody(t *testing.T) {
	body := []byte(strings.Repeat("x", 2000))
	error := NewHTTPStatusError(http.StatusBadGateway, body, nil)
	message := error.Error()
	if !strings.HasPrefix(message, "HTTP 502: ") {
		t.Fatalf("前缀 = %q", message[:16])
	}
	if len(message) != len("HTTP 502: ")+512 {
		t.Fatalf("正文应截断到 512 字节，实际 %d", len(message)-len("HTTP 502: "))
	}
}

func TestHTTPStatusErrorRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
	}
	for _, test := range tests {
		if got := NewHTTPStatusError(test.status, nil, nil).Retryable(); got != test.want {
			t.Errorf("status %d: Retryable = %v, want %v", test.status, got, test.want)
		}
	}
	var nilError *HTTPStatusError
	if nilError.Retryable() {
		t.Error("nil 接收者必须返回 false")
	}
	if nilError.Error() != "HTTP error" {
		t.Errorf("nil 接收者的 Error() = %q", nilError.Error())
	}
}

func TestHTTPStatusErrorParsesRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{name: "无 header", header: nil, want: 0},
		{name: "空值", header: http.Header{"Retry-After": []string{""}}, want: 0},
		{name: "秒数", header: http.Header{"Retry-After": []string{"7"}}, want: 7 * time.Second},
		{name: "带空格秒数", header: http.Header{"Retry-After": []string{" 3 "}}, want: 3 * time.Second},
		{name: "零与负数忽略", header: http.Header{"Retry-After": []string{"0"}}, want: 0},
		{name: "非法值忽略", header: http.Header{"Retry-After": []string{"later"}}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			error := NewHTTPStatusError(http.StatusTooManyRequests, nil, test.header)
			if error.RetryAfter != test.want {
				t.Fatalf("RetryAfter = %v, want %v", error.RetryAfter, test.want)
			}
		})
	}

	// HTTP-date 形式。
	when := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	error := NewHTTPStatusError(http.StatusTooManyRequests, nil, http.Header{"Retry-After": []string{when}})
	if error.RetryAfter < 25*time.Second || error.RetryAfter > 31*time.Second {
		t.Fatalf("HTTP-date 解析 = %v, 期望约 30s", error.RetryAfter)
	}

	// 已过去的日期不产生等待。
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	error = NewHTTPStatusError(http.StatusTooManyRequests, nil, http.Header{"Retry-After": []string{past}})
	if error.RetryAfter != 0 {
		t.Fatalf("过期日期应解析为 0，实际 %v", error.RetryAfter)
	}
}

func TestHTTPStatusErrorIsClassifiable(t *testing.T) {
	wrapped := fmt.Errorf("ChatClient: %w", NewHTTPStatusError(http.StatusTooManyRequests, []byte("slow down"), nil))
	var statusError *HTTPStatusError
	if !errors.As(wrapped, &statusError) {
		t.Fatal("errors.As 必须能取出类型化状态错误")
	}
	if statusError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d", statusError.StatusCode)
	}
	if !strings.Contains(wrapped.Error(), "HTTP 429: slow down") {
		t.Fatalf("包装后的文案 = %q", wrapped.Error())
	}
}
