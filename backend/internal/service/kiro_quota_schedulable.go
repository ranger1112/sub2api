package service

import (
	"fmt"
	"time"
)

const (
	// kiroQuotaExhaustedPercent 达到即视为当前窗口配额耗尽(Kiro 上游是请求数配额,100% = 用满)。
	kiroQuotaExhaustedPercent = 100.0
	// kiroQuotaSnapshotStaleAfter 是用量快照的陈旧上限:超过则不再据其硬跳过,避免一个长期空闲、
	// 快照永不刷新的账号被永久排除(对齐 openAICodexAutoPauseStaleAfter=2h)。
	kiroQuotaSnapshotStaleAfter = 2 * time.Hour
)

// kiroQuotaExhausted 判定一个 Kiro 账号是否因订阅窗口配额耗尽而应被「主动跳过」调度
// (对齐 Gemini 的 PreCheckUsage 硬跳过——都是请求数配额)。数据来自 buildKiroUsageExtraUpdates
// 落库的 kiro_* 快照。以下情形返回 false(仍可调度),内建自愈,避免误伤/永久排除:
//   - 非 Kiro / 无 Extra / 无 kiro_usage_used_percent → 无依据,放行;
//   - used_percent < 100 → 未耗尽;
//   - kiro_usage_reset_at 已过(窗口已滚动)→ 旧百分比失效,放行;
//   - 快照陈旧(kiro_usage_updated_at 距今 ≥ 2h,账号可能长期空闲未刷新)→ 放行,下次成功响应自愈。
//
// 只做「已耗尽」硬跳过,不做 <100% 的软降权(通用调度器无加权评分位可插;需要时另议)。
func kiroQuotaExhausted(account *Account, now time.Time) bool {
	if account == nil || account.Platform != PlatformKiro || len(account.Extra) == 0 {
		return false
	}
	usedPercent, ok := resolveAccountExtraNumber(account.Extra, "kiro_usage_used_percent")
	if !ok || usedPercent < kiroQuotaExhaustedPercent {
		return false
	}
	// 窗口已滚动 → 旧百分比失效,当作健康。
	if resetRaw, ok := account.Extra["kiro_usage_reset_at"]; ok {
		if resetAt, err := parseTime(fmt.Sprint(resetRaw)); err == nil && !now.Before(resetAt) {
			return false
		}
	}
	// 快照陈旧(长期空闲未刷新)→ 不永久排除,交由下次成功响应自愈。
	if updatedRaw, ok := account.Extra["kiro_usage_updated_at"]; ok {
		if updatedAt, err := parseTime(fmt.Sprint(updatedRaw)); err == nil &&
			now.Sub(updatedAt) >= kiroQuotaSnapshotStaleAfter {
			return false
		}
	}
	return true
}
