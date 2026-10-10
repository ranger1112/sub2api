/**
 * Shared display formatters for channel-monitor-v2.
 * Kept pure so unit tests can lock metric presentation accuracy.
 *
 * Privacy: prefer rates (error_rate, RPM, TPM, success %) over absolute
 * request/error/token counts in user-facing surfaces.
 */

import type { HealthScoreBand, HealthState, MonitorHealth, MonitorMetric } from '@/api/channelMonitorV2'
import { formatCompactNumber } from '@/utils/format'

export function monitorIntlLocale(): string {
  if (typeof document !== 'undefined') {
    const htmlLang = document.documentElement.getAttribute('lang')?.trim()
    if (htmlLang) return htmlLang
  }
  if (typeof navigator !== 'undefined' && navigator.language) return navigator.language
  return 'zh-CN'
}

export function formatMonitorNumber(value: number, compactAt = 10000, locale = monitorIntlLocale()): string {
  return Intl.NumberFormat(locale, {
    notation: value >= compactAt ? 'compact' : 'standard',
    maximumFractionDigits: 1,
  }).format(value || 0)
}

export function formatMonitorRate(value: number, locale = monitorIntlLocale()): string {
  return Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value || 0)
}

/**
 * RPM/TPM throughput display: always K/M compact (1.0K, 1.5M) for ops-readable density.
 * Prefer this over formatMonitorRate for rate KPIs and table cells.
 */
export function formatMonitorThroughput(value: number | null | undefined): string {
  if (value == null || Number.isNaN(Number(value))) return '0'
  const n = Number(value)
  if (Math.abs(n) < 1000) {
    return Intl.NumberFormat(monitorIntlLocale(), { maximumFractionDigits: 1 }).format(n)
  }
  return formatCompactNumber(n)
}

/**
 * Backend stores tokens-per-minute as `tpm`. Convert to tokens-per-second for display.
 */
export function tokensPerSecondFromTpm(tpm: number | null | undefined): number {
  if (tpm == null || Number.isNaN(Number(tpm))) return 0
  return Number(tpm) / 60
}

/** Tokens/sec from backend TPM (per-minute), compact ops formatting. */
export function formatMonitorTokensPerSecond(tpm: number | null | undefined): string {
  return formatMonitorThroughput(tokensPerSecondFromTpm(tpm))
}


export function formatMonitorPercent(value: number, locale = monitorIntlLocale()): string {
  return `${new Intl.NumberFormat(locale, {
    minimumFractionDigits: value < 0.01 ? 2 : 1,
    maximumFractionDigits: value < 0.01 ? 2 : 1,
  }).format((value || 0) * 100)}%`
}

export function formatMonitorMs(value: number | null | undefined): string {
  if (value == null) return '-'
  return value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}ms`
}

export function formatMonitorSuccessRate(successRequests: number, requestCount: number): string {
  if (!requestCount) return '-'
  return formatMonitorPercent(successRequests / requestCount)
}

export function formatMonitorSuccessRateFromError(errorRate: number): string {
  return formatMonitorPercent(1 - (errorRate || 0))
}

/**
 * Map continuous 0–100 score to 11 fine bands for multi-stop green→yellow→red.
 * score10 = best (green), score0 = worst (red).
 */
export function scoreToBand(score: number | null | undefined): HealthScoreBand {
  if (score == null || Number.isNaN(score)) return 'unknown'
  const clamped = Math.max(0, Math.min(100, score))
  // 0–100 → score0..score10 (11 stops for green→yellow→red gradient)
  const band = Math.round(clamped / 10)
  return `score${Math.max(0, Math.min(10, band))}` as HealthScoreBand
}

export type HealthDisplayMode = 'overall' | 'success' | 'ttft' | 'cache'

/** Resolve the score used for a health mode. */
export function healthModeScore(
  health: MonitorHealth,
  mode: HealthDisplayMode,
): number | null {
  if (mode === 'success') {
    return health.error_rate_score ?? null
  }
  if (mode === 'ttft') {
    return health.ttft_score ?? null
  }
  if (mode === 'cache') {
    return health.cache_score ?? null
  }
  return health.score ?? null
}

export function healthScoreClass(
  health: MonitorHealth,
  mode: HealthDisplayMode,
  requestCount: number,
  metric?: MonitorMetric,
): string {
  const score = healthModeScore(health, mode)
  // Sync-only traffic has no first-token samples. Never paint TTFT as red/empty-fail.
  if (mode === 'ttft' && score == null && metric?.ttft?.p50_ms == null) return 'health-unknown'
  if (score == null) {
    // A public response may redact request_count, and a short bucket may be
    // below the server's minimum scoring sample. Preserve a visible status for
    // buckets that still carry metrics instead of turning all traffic gray.
    const coarse =
      mode === 'success'
        ? health.error_rate
        : mode === 'cache'
          ? health.cache
          : mode === 'ttft'
            ? health.ttft
            : health.overall
    if (coarse !== 'unknown') return healthStateClass(coarse)
    if (requestCount <= 0 && !hasMonitorTraffic(metric)) return 'health-unknown'
    return provisionalHealthClass(health, mode, metric)
  }
  return `health-${scoreToBand(score)}`
}

function hasMonitorTraffic(metric?: MonitorMetric): boolean {
  if (!metric) return false
  return Boolean(
    metric.request_count > 0 ||
      metric.rpm > 0 ||
      metric.tpm > 0 ||
      metric.error_rate > 0 ||
      metric.ttft.p50_ms != null ||
      metric.duration.p50_ms != null,
  )
}

/** Coarse fallback for low-volume buckets whose exact score is unavailable. */
function provisionalHealthClass(
  health: MonitorHealth,
  mode: HealthDisplayMode,
  metric?: MonitorMetric,
): string {
  if (!metric) return 'health-unknown'
  const thresholds = health.thresholds
  if (mode === 'success') {
    const errorRate = metric.error_rate || 0
    if (errorRate >= (thresholds?.critical_error_rate ?? 0.05)) return 'health-critical'
    if (errorRate >= (thresholds?.warning_error_rate ?? 0.02)) return 'health-warning'
    return 'health-healthy'
  }
  if (mode === 'cache') {
    const cacheRate = metric.cache_rate || 0
    if (cacheRate < (thresholds?.critical_cache_rate ?? 0.05)) return 'health-critical'
    if (cacheRate < (thresholds?.warning_cache_rate ?? 0.2)) return 'health-warning'
    return 'health-healthy'
  }
  if (mode === 'ttft') {
    const p50 = metric.ttft.p50_ms
    if (p50 == null) return 'health-unknown'
    if (p50 >= (thresholds?.critical_ttft_ms ?? 6000)) return 'health-critical'
    if (p50 >= (thresholds?.warning_ttft_ms ?? 4000)) return 'health-warning'
    return 'health-healthy'
  }
  // Overall fallback follows the server's coarse health inputs. Error rate is
  // the safest signal when a bucket is too small for a weighted score.
  const errorRate = metric.error_rate || 0
  if (errorRate >= (thresholds?.critical_error_rate ?? 0.05)) return 'health-critical'
  if (errorRate >= (thresholds?.warning_error_rate ?? 0.02)) return 'health-warning'
  return 'health-healthy'
}

/** Missing first-token samples are "not applicable", not a failed latency budget. */
export function isTtftUnavailable(ttft?: { p50_ms?: number | null; sample_count?: number } | null): boolean {
  if (ttft == null) return true
  return ttft.p50_ms == null
}

export function ttftDisplayState(
  state: HealthState | undefined,
  ttft?: { p50_ms?: number | null; sample_count?: number } | null,
): HealthState | undefined {
  if (isTtftUnavailable(ttft)) return 'unknown'
  return state
}

export function healthStateClass(state: string | undefined): string {
  return `health-${state || 'unknown'}`
}

/** Privacy-safe latency summary: avg + p50 + p90 (no absolute sample counts). */
export function formatLatencyPrivacy(
  p50: number | null | undefined,
  p90: number | null | undefined,
  avg?: number | null | undefined,
  p95?: number | null | undefined,
): string {
  const parts: string[] = []
  if (avg != null) parts.push(`AVG ${formatMonitorMs(avg)}`)
  if (p50 != null) parts.push(`P50 ${formatMonitorMs(p50)}`)
  if (p90 != null) parts.push(`P90 ${formatMonitorMs(p90)}`)
  // p95 only as fallback when p90 missing (older payloads)
  if (p90 == null && p95 != null) parts.push(`P95 ${formatMonitorMs(p95)}`)
  return parts.length ? parts.join(' · ') : '-'
}

/**
 * KPI secondary line for latency: AVG + P90 only (P50 is the primary value).
 * Falls back to P95 when P90 is absent. Delimiter is " · " so MetricCell can
 * split into non-truncated chips.
 */
export function formatLatencyKpiSecondary(
  avg?: number | null | undefined,
  p90?: number | null | undefined,
  p95?: number | null | undefined,
): string {
  const parts: string[] = []
  if (avg != null && Number.isFinite(avg)) parts.push(`AVG ${formatMonitorMs(avg)}`)
  if (p90 != null && Number.isFinite(p90)) parts.push(`P90 ${formatMonitorMs(p90)}`)
  else if (p95 != null && Number.isFinite(p95)) parts.push(`P95 ${formatMonitorMs(p95)}`)
  return parts.length ? parts.join(' · ') : '-'
}

/** Fallback when i18n key is missing; prefer `channelMonitorV2.errorCategories.*`. */
export function monitorErrorCategoryLabel(category: string): string {
  return category
}
