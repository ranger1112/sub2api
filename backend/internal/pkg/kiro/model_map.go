package kiro

import "strings"

// Kiro 上游实际服务的 Claude 模型 ID(Kiro/Amazon Q 底层即 Claude)。
const (
	ModelSonnet45 = "claude-sonnet-4.5"
	ModelSonnet46 = "claude-sonnet-4.6"
	ModelSonnet5  = "claude-sonnet-5"
	ModelOpus45   = "claude-opus-4.5"
	ModelOpus46   = "claude-opus-4.6"
	ModelOpus47   = "claude-opus-4.7"
	ModelOpus48   = "claude-opus-4.8"
	ModelHaiku45  = "claude-haiku-4.5"
)

const (
	// ContextWindowDefault 是多数 Claude 模型的上下文窗口(200K)。
	ContextWindowDefault = 200_000
	// ContextWindowLarge 是 sonnet-4.6 / opus-4.6+ 的上下文窗口(1M,Kiro 自 2026-03-24 起)。
	ContextWindowLarge = 1_000_000
)

// DefaultThinkingSuffix 是识别 thinking 模式的默认模型名后缀。
const DefaultThinkingSuffix = "-thinking"

// ServedModels 返回 Kiro 上游实际服务的规范模型 ID 列表(与 MapModel 的输出集合一致)。
// 用于账号「可用模型」列表与「同步上游模型」:Kiro 无 /v1/models 端点、模型集固定,
// 直接返回该静态集合,避免把 Kiro 不服务的 Claude 老模型暴露到下拉/映射里。
func ServedModels() []string {
	return []string{
		ModelSonnet5,
		ModelSonnet46,
		ModelSonnet45,
		ModelOpus48,
		ModelOpus47,
		ModelOpus46,
		ModelOpus45,
		ModelHaiku45,
	}
}

// MapModel 把客户端传入的 Anthropic 模型名映射到 Kiro 支持的模型 ID。
//
// 采用严格版本匹配(对照 kiro.rs 的 map_model 实现):Kiro 仅服务
// sonnet 4.5/4.6、opus 4.5/4.6/4.7/4.8、haiku 4.5。无法识别的名称(如
// gpt-*、裸 sonnet-4、claude-3-5-* 等)返回 ("", false),调用方据此返回
// model_not_found;若需把这些别名归一化到 Kiro 模型,由 sub2api 的渠道/分组
// 级模型映射在网关层处理。
//
// 注:模型名中的 "-thinking" 后缀不影响映射(仅做子串匹配),thinking 与否
// 由 ParseThinking / 请求体单独判定。
func MapModel(model string) (string, bool) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "sonnet"):
		switch {
		case strings.Contains(m, "4-6") || strings.Contains(m, "4.6"):
			return ModelSonnet46, true
		case strings.Contains(m, "4-5") || strings.Contains(m, "4.5"):
			return ModelSonnet45, true
		// Sonnet 5(须紧邻 "sonnet" 以避开旧的 "claude-3-5-sonnet")。
		case strings.Contains(m, "sonnet-5") || strings.Contains(m, "sonnet5") ||
			strings.Contains(m, "sonnet.5") || strings.Contains(m, "sonnet 5") ||
			strings.Contains(m, "sonnet_5"):
			return ModelSonnet5, true
		default:
			return "", false
		}
	case strings.Contains(m, "opus"):
		switch {
		case strings.Contains(m, "4-5") || strings.Contains(m, "4.5"):
			return ModelOpus45, true
		case strings.Contains(m, "4-6") || strings.Contains(m, "4.6"):
			return ModelOpus46, true
		case strings.Contains(m, "4-7") || strings.Contains(m, "4.7"):
			return ModelOpus47, true
		case strings.Contains(m, "4-8") || strings.Contains(m, "4.8"):
			return ModelOpus48, true
		default:
			return "", false
		}
	case strings.Contains(m, "haiku"):
		return ModelHaiku45, true
	default:
		return "", false
	}
}

// ContextWindowSize 返回模型对应的上下文窗口大小,复用 MapModel 保持一致。
// 未识别的模型按默认 200K 处理。
func ContextWindowSize(model string) int {
	mapped, ok := MapModel(model)
	if !ok {
		return ContextWindowDefault
	}
	switch mapped {
	case ModelSonnet5, ModelSonnet46, ModelOpus46, ModelOpus47, ModelOpus48:
		return ContextWindowLarge
	default:
		return ContextWindowDefault
	}
}

// ParseThinking 判断模型名是否带 thinking 后缀,并返回去除后缀后的模型名。
// suffix 为空时使用 DefaultThinkingSuffix。MapModel 本身对后缀不敏感,此函数
// 仅用于向上游传递 thinking 意图。
func ParseThinking(model, suffix string) (base string, thinking bool) {
	if suffix == "" {
		suffix = DefaultThinkingSuffix
	}
	if strings.HasSuffix(strings.ToLower(model), suffix) {
		return model[:len(model)-len(suffix)], true
	}
	return model, false
}
