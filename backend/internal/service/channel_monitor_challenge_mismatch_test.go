//go:build unit

package service

import (
	"strings"
	"testing"
)

// TestClassifyChallengeMismatch 覆盖 challenge 校验失败的状态分类 + 诊断消息:
//   - respText 非空(模型算错)   → failed,保留 "challenge mismatch (expected N, got %q)"
//   - respText 为空(2xx 错误信封)→ error,回填上游 error 文案 / 原始片段 / "empty body"
func TestClassifyChallengeMismatch(t *testing.T) {
	const expected = "75"

	tests := []struct {
		name       string
		respText   string
		rawBody    string
		wantStatus string
		wantSubstr string
	}{
		{
			name:       "non-empty wrong answer stays failed",
			respText:   "42",
			rawBody:    `{"content":[{"text":"42"}]}`,
			wantStatus: MonitorStatusFailed,
			wantSubstr: `challenge mismatch (expected 75, got "42")`,
		},
		{
			name:       "empty text with nested error.message is error",
			respText:   "",
			rawBody:    `{"error":{"message":"No available accounts in pool"}}`,
			wantStatus: MonitorStatusError,
			wantSubstr: "No available accounts in pool",
		},
		{
			name:       "empty text with string error is error",
			respText:   "",
			rawBody:    `{"error":"upstream boom"}`,
			wantStatus: MonitorStatusError,
			wantSubstr: "upstream boom",
		},
		{
			name:       "empty text with top-level message is error",
			respText:   "",
			rawBody:    `{"message":"rate limited, retry later"}`,
			wantStatus: MonitorStatusError,
			wantSubstr: "rate limited, retry later",
		},
		{
			name:       "empty text with non-json body surfaces snippet",
			respText:   "",
			rawBody:    "Bad Gateway",
			wantStatus: MonitorStatusError,
			wantSubstr: "no answer text",
		},
		{
			name:       "empty text with non-json body includes the body",
			respText:   "",
			rawBody:    "Bad Gateway",
			wantStatus: MonitorStatusError,
			wantSubstr: "Bad Gateway",
		},
		{
			name:       "empty text and empty body reports empty body",
			respText:   "",
			rawBody:    "",
			wantStatus: MonitorStatusError,
			wantSubstr: "empty body",
		},
		{
			name:       "whitespace-only text is treated as empty (error)",
			respText:   "   \n\t ",
			rawBody:    `{"error":{"message":"pool drained"}}`,
			wantStatus: MonitorStatusError,
			wantSubstr: "pool drained",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := classifyChallengeMismatch(expected, tt.respText, tt.rawBody)
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (msg=%q)", status, tt.wantStatus, msg)
			}
			if !strings.Contains(msg, tt.wantSubstr) {
				t.Fatalf("message %q does not contain %q", msg, tt.wantSubstr)
			}
			// 每条消息都应带上 expected,便于运维定位。
			if !strings.Contains(msg, expected) {
				t.Fatalf("message %q should mention expected %q", msg, expected)
			}
		})
	}
}
