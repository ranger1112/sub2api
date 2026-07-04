package kiro

import "testing"

func TestMapModel(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"claude-sonnet-4-5-20250929", ModelSonnet45, true},
		{"claude-sonnet-4.5", ModelSonnet45, true},
		{"claude-sonnet-4-6", ModelSonnet46, true},
		{"claude-sonnet-4.6", ModelSonnet46, true},
		{"claude-sonnet-5", ModelSonnet5, true},
		{"claude-sonnet-5-20250929", ModelSonnet5, true},
		{"claude-sonnet-5-thinking", ModelSonnet5, true},
		{"claude-opus-4-5-20251101", ModelOpus45, true},
		{"claude-opus-4-6", ModelOpus46, true},
		{"claude-opus-4-7", ModelOpus47, true},
		{"claude-opus-4-8", ModelOpus48, true},
		{"claude-opus-4-8-thinking", ModelOpus48, true}, // 后缀不影响映射
		{"claude-haiku-4-5-20251001", ModelHaiku45, true},
		{"claude-haiku-4-20250514", ModelHaiku45, true}, // haiku 不校验版本号
		{"CLAUDE-SONNET-4.6", ModelSonnet46, true},      // 大小写不敏感
		// 未识别:与 kiro.rs 实现一致(Kiro 不服务这些),网关层渠道映射可另行归一化。
		{"gpt-4", "", false},
		{"claude-sonnet-4-20250514", "", false}, // 裸 Sonnet 4,非 4.5/4.6
		{"claude-3-5-sonnet-20241022", "", false},
		{"claude-opus-4-20250514", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := MapModel(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("MapModel(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestContextWindowSize(t *testing.T) {
	for _, m := range []string{"claude-sonnet-5", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8"} {
		if got := ContextWindowSize(m); got != ContextWindowLarge {
			t.Errorf("ContextWindowSize(%q) = %d, want %d", m, got, ContextWindowLarge)
		}
	}
	for _, m := range []string{"claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5", "gpt-4", "unknown"} {
		if got := ContextWindowSize(m); got != ContextWindowDefault {
			t.Errorf("ContextWindowSize(%q) = %d, want %d", m, got, ContextWindowDefault)
		}
	}
}

func TestParseThinking(t *testing.T) {
	if base, thinking := ParseThinking("claude-opus-4-8-thinking", ""); !thinking || base != "claude-opus-4-8" {
		t.Fatalf("got (%q, %v), want (claude-opus-4-8, true)", base, thinking)
	}
	if base, thinking := ParseThinking("claude-opus-4-8", ""); thinking || base != "claude-opus-4-8" {
		t.Fatalf("got (%q, %v), want (claude-opus-4-8, false)", base, thinking)
	}
	// 自定义后缀
	if base, thinking := ParseThinking("claude-sonnet-4-6:think", ":think"); !thinking || base != "claude-sonnet-4-6" {
		t.Fatalf("custom suffix: got (%q, %v)", base, thinking)
	}
}
