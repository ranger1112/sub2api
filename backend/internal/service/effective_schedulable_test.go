//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateEffectiveSchedulable_BlocksStaticCooldowns(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	resetAt := now.Add(time.Hour)
	account := &Account{
		ID:               776,
		Status:           StatusActive,
		Schedulable:      true,
		RateLimitResetAt: &resetAt,
	}

	decision := EvaluateEffectiveSchedulable(account, EffectiveSchedulableRuntimeHealth{}, now, EffectiveSchedulableConfig{})

	require.False(t, decision.Schedulable)
	require.Contains(t, decision.Reasons, effectiveSchedulableReasonRateLimited)
	require.NotNil(t, decision.CooldownUntil)
	require.Equal(t, resetAt, *decision.CooldownUntil)
}

func TestEvaluateEffectiveSchedulable_BlocksRuntimeCooldown(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	blockUntil := now.Add(2 * time.Minute)
	account := &Account{ID: 42, Status: StatusActive, Schedulable: true}

	decision := EvaluateEffectiveSchedulable(account, EffectiveSchedulableRuntimeHealth{
		RuntimeBlockedUntil: &blockUntil,
	}, now, EffectiveSchedulableConfig{})

	require.False(t, decision.Schedulable)
	require.Contains(t, decision.Reasons, effectiveSchedulableReasonRuntimeCooldown)
	require.Equal(t, blockUntil, *decision.CooldownUntil)
}

func TestEvaluateEffectiveSchedulable_DegradesHighTTFTWithoutBlocking(t *testing.T) {
	account := &Account{ID: 42, Status: StatusActive, Schedulable: true}

	decision := EvaluateEffectiveSchedulable(account, EffectiveSchedulableRuntimeHealth{
		TTFTMS:  30000,
		HasTTFT: true,
	}, time.Now(), EffectiveSchedulableConfig{TTFTDegradeThresholdMS: 15000})

	require.True(t, decision.Schedulable)
	require.Contains(t, decision.Reasons, effectiveSchedulableReasonHighTTFT)
	require.InDelta(t, 0.5, decision.WeightMultiplier, 0.001)
}

func TestEvaluateEffectiveSchedulable_BlocksExtremeErrorRate(t *testing.T) {
	account := &Account{ID: 42, Status: StatusActive, Schedulable: true}

	decision := EvaluateEffectiveSchedulable(account, EffectiveSchedulableRuntimeHealth{
		ErrorRate:    0.97,
		HasErrorRate: true,
	}, time.Now(), EffectiveSchedulableConfig{
		ErrorRateDegradeThreshold: 0.5,
		ErrorRateBlockThreshold:   0.95,
	})

	require.False(t, decision.Schedulable)
	require.Contains(t, decision.Reasons, effectiveSchedulableReasonHighErrorRate)
}

func TestEffectiveLoadRate_AppliesWeightPenalty(t *testing.T) {
	require.Equal(t, 10, effectiveLoadRate(10, 1))
	require.Equal(t, 60, effectiveLoadRate(10, 0.5))
	require.Equal(t, 100, effectiveLoadRate(80, 0.5))
	require.Equal(t, 10, effectiveLoadRate(10, 0))
}
