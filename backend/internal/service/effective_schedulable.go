package service

import (
	"log/slog"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	effectiveSchedulableReasonNilAccount       = "nil_account"
	effectiveSchedulableReasonInactive         = "inactive"
	effectiveSchedulableReasonDisabled         = "schedulable_disabled"
	effectiveSchedulableReasonExpired          = "expired"
	effectiveSchedulableReasonOverloaded       = "overloaded"
	effectiveSchedulableReasonRateLimited      = "rate_limited"
	effectiveSchedulableReasonTempUnsched      = "temp_unschedulable"
	effectiveSchedulableReasonQuotaExceeded    = "quota_exceeded"
	effectiveSchedulableReasonRuntimeCooldown  = "runtime_cooldown"
	effectiveSchedulableReasonHighErrorRate    = "high_error_rate"
	effectiveSchedulableReasonElevatedError    = "elevated_error_rate"
	effectiveSchedulableReasonHighTTFT         = "high_ttft"
	defaultEffectiveSchedulableTTFTThresholdMS = 15000
)

// EffectiveSchedulableConfig controls the first-stage runtime health overlay.
// Enabled decides whether runtime overlay changes scheduling; ShadowEnabled
// only emits decision logs and keeps the historical selection intact.
type EffectiveSchedulableConfig struct {
	Enabled                   bool
	ShadowEnabled             bool
	TTFTDegradeThresholdMS    int
	ErrorRateDegradeThreshold float64
	ErrorRateBlockThreshold   float64
}

type EffectiveSchedulableRuntimeHealth struct {
	RuntimeBlockedUntil *time.Time
	ErrorRate           float64
	HasErrorRate        bool
	TTFTMS              float64
	HasTTFT             bool
}

type EffectiveSchedulableDecision struct {
	Schedulable      bool
	Reasons          []string
	WeightMultiplier float64
	CooldownUntil    *time.Time
}

func effectiveSchedulableConfigFromScheduling(cfg config.GatewaySchedulingConfig) EffectiveSchedulableConfig {
	ttftThreshold := cfg.EffectiveSchedulableTTFTDegradeThresholdMS
	if ttftThreshold <= 0 {
		ttftThreshold = defaultEffectiveSchedulableTTFTThresholdMS
	}
	degradeThreshold := cfg.EffectiveSchedulableErrorRateDegradeThreshold
	blockThreshold := cfg.EffectiveSchedulableErrorRateBlockThreshold
	if blockThreshold > 0 && degradeThreshold > 0 && blockThreshold < degradeThreshold {
		blockThreshold = degradeThreshold
	}
	return EffectiveSchedulableConfig{
		Enabled:                   cfg.EffectiveSchedulableEnabled,
		ShadowEnabled:             cfg.EffectiveSchedulableShadowEnabled,
		TTFTDegradeThresholdMS:    ttftThreshold,
		ErrorRateDegradeThreshold: clamp01(degradeThreshold),
		ErrorRateBlockThreshold:   clamp01(blockThreshold),
	}
}

func EvaluateEffectiveSchedulable(account *Account, health EffectiveSchedulableRuntimeHealth, now time.Time, cfg EffectiveSchedulableConfig) EffectiveSchedulableDecision {
	if now.IsZero() {
		now = time.Now()
	}
	decision := EffectiveSchedulableDecision{
		Schedulable:      true,
		WeightMultiplier: 1,
	}

	addHardReason := func(reason string) {
		decision.Schedulable = false
		decision.Reasons = append(decision.Reasons, reason)
	}
	addSoftReason := func(reason string, multiplier float64) {
		decision.Reasons = append(decision.Reasons, reason)
		if multiplier > 0 && multiplier < decision.WeightMultiplier {
			decision.WeightMultiplier = multiplier
		}
	}

	if account == nil {
		addHardReason(effectiveSchedulableReasonNilAccount)
		return decision
	}
	if !account.IsActive() {
		addHardReason(effectiveSchedulableReasonInactive)
	}
	if !account.Schedulable {
		addHardReason(effectiveSchedulableReasonDisabled)
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		addHardReason(effectiveSchedulableReasonExpired)
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		decision.CooldownUntil = laterTime(decision.CooldownUntil, *account.OverloadUntil)
		addHardReason(effectiveSchedulableReasonOverloaded)
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		decision.CooldownUntil = laterTime(decision.CooldownUntil, *account.RateLimitResetAt)
		addHardReason(effectiveSchedulableReasonRateLimited)
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		decision.CooldownUntil = laterTime(decision.CooldownUntil, *account.TempUnschedulableUntil)
		addHardReason(effectiveSchedulableReasonTempUnsched)
	}
	if account.IsAPIKeyOrBedrock() && account.IsQuotaExceeded() {
		addHardReason(effectiveSchedulableReasonQuotaExceeded)
	}

	if health.RuntimeBlockedUntil != nil && now.Before(*health.RuntimeBlockedUntil) {
		decision.CooldownUntil = laterTime(decision.CooldownUntil, *health.RuntimeBlockedUntil)
		addHardReason(effectiveSchedulableReasonRuntimeCooldown)
	}

	if health.HasErrorRate {
		errorRate := clamp01(health.ErrorRate)
		switch {
		case cfg.ErrorRateBlockThreshold > 0 && errorRate >= cfg.ErrorRateBlockThreshold:
			addHardReason(effectiveSchedulableReasonHighErrorRate)
		case cfg.ErrorRateDegradeThreshold > 0 && errorRate >= cfg.ErrorRateDegradeThreshold:
			addSoftReason(effectiveSchedulableReasonElevatedError, math.Max(0.1, 1-errorRate))
		}
	}
	if health.HasTTFT && cfg.TTFTDegradeThresholdMS > 0 && health.TTFTMS > float64(cfg.TTFTDegradeThresholdMS) {
		// 高 TTFT 先降权，不直接剔除；超过阈值越多，权重越低，最低保留 20%。
		ratio := float64(cfg.TTFTDegradeThresholdMS) / health.TTFTMS
		addSoftReason(effectiveSchedulableReasonHighTTFT, math.Max(0.2, clamp01(ratio)))
	}
	return decision
}

func laterTime(current *time.Time, candidate time.Time) *time.Time {
	c := candidate
	if current == nil || candidate.After(*current) {
		return &c
	}
	return current
}

func effectiveLoadRate(loadRate int, multiplier float64) int {
	if multiplier <= 0 {
		multiplier = 1
	}
	if multiplier >= 1 {
		return loadRate
	}
	adjusted := float64(loadRate) + (1-multiplier)*100
	if adjusted > 100 {
		return 100
	}
	return int(math.Round(adjusted))
}

func logEffectiveSchedulableShadow(scope string, accountID int64, decision EffectiveSchedulableDecision, applied bool) {
	if len(decision.Reasons) == 0 {
		return
	}
	slog.Debug("effective_schedulable_decision",
		"scope", scope,
		"account_id", accountID,
		"schedulable", decision.Schedulable,
		"weight_multiplier", decision.WeightMultiplier,
		"reasons", decision.Reasons,
		"cooldown_until", decision.CooldownUntil,
		"applied", applied,
	)
}
