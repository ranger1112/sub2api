//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpenAICapacityQuarantineStoreForTest(t *testing.T) (*openAICapacityQuarantineStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &openAICapacityQuarantineStore{rdb: rdb}, mr
}

func TestOpenAICapacityQuarantineStore_DeduplicatesRollingEventsAndDistinctGroupSpike(t *testing.T) {
	ctx := context.Background()
	store, _ := newOpenAICapacityQuarantineStoreForTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	count, duplicate, err := store.RecordOpenAICapacityEvent(ctx, 11, "request-a", now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.False(t, duplicate)

	count, duplicate, err = store.RecordOpenAICapacityEvent(ctx, 11, "request-a", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.True(t, duplicate, "same request/account must count at most once")

	count, duplicate, err = store.RecordOpenAICapacityEvent(ctx, 11, "request-b", now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, count, "expired rolling-window event must be pruned")
	require.False(t, duplicate)

	distinct, err := store.RecordOpenAICapacityGroupEvent(ctx, 7, "gpt-5", 11, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, distinct)
	distinct, err = store.RecordOpenAICapacityGroupEvent(ctx, 7, "gpt-5", 11, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, distinct, "group spike counts distinct accounts, not event volume")
	distinct, err = store.RecordOpenAICapacityGroupEvent(ctx, 7, "gpt-5", 12, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, distinct)

	// Repeated events from the same account extend that account's presence in a
	// rolling distinct-account window. They must not leave an old score behind
	// and make a still-active account disappear early.
	distinct, err = store.RecordOpenAICapacityGroupEvent(ctx, 8, "gpt-5", 11, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, distinct)
	distinct, err = store.RecordOpenAICapacityGroupEvent(ctx, 8, "gpt-5", 11, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, distinct)
	distinct, err = store.RecordOpenAICapacityGroupEvent(ctx, 8, "gpt-5", 12, now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, distinct)
}

func TestOpenAICapacityQuarantineStore_OpenDoesNotExtendAndHalfOpenOwnerIsExclusive(t *testing.T) {
	ctx := context.Background()
	store, mr := newOpenAICapacityQuarantineStoreForTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	opened, first, err := store.OpenOpenAICapacityQuarantine(ctx, service.OpenAICapacityQuarantineOpenRequest{
		AccountID: 17, Now: now, Cooldown: time.Minute, State: "open_initial", RuleID: "capacity",
	})
	require.NoError(t, err)
	require.True(t, opened)
	require.Equal(t, "open_initial", first.State)
	require.Equal(t, now.UnixMilli(), first.OpenedAt.UnixMilli())

	opened, _, err = store.OpenOpenAICapacityQuarantine(ctx, service.OpenAICapacityQuarantineOpenRequest{
		AccountID: 17, Now: now.Add(10 * time.Second), Cooldown: 5 * time.Minute, State: "open_retrip", RuleID: "capacity",
	})
	require.NoError(t, err)
	require.False(t, opened, "late error must not extend an already-open cooldown")
	state, err := store.GetOpenAICapacityQuarantineState(ctx, 17)
	require.NoError(t, err)
	require.Equal(t, first.CooldownUntil.UnixMilli(), state.CooldownUntil.UnixMilli())
	require.Equal(t, first.OpenedAt.UnixMilli(), state.OpenedAt.UnixMilli())

	mr.FastForward(time.Minute)
	acquired, err := store.AcquireOpenAICapacityProbe(ctx, 17, "owner-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = store.AcquireOpenAICapacityProbe(ctx, 17, "owner-b", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "half-open permits exactly one owner")

	renewed, err := store.RenewOpenAICapacityProbe(ctx, 17, "owner-b", time.Minute)
	require.NoError(t, err)
	require.False(t, renewed, "only the owner may renew")
	completed, err := store.CompleteOpenAICapacityProbe(ctx, 17, "owner-b", time.Now())
	require.NoError(t, err)
	require.False(t, completed, "only the owner may complete")

	completed, err = store.CompleteOpenAICapacityProbe(ctx, 17, "owner-a", time.Now())
	require.NoError(t, err)
	require.True(t, completed)
	state, err = store.GetOpenAICapacityQuarantineState(ctx, 17)
	require.NoError(t, err)
	require.Equal(t, "closed", state.State)
	_, err = store.rdb.ZCard(ctx, store.eventsKey(17)).Result()
	require.NoError(t, err)
}

func TestOpenAICapacityQuarantineStore_RetripRevokesPreviousProbeOwnerAndTripLocksAreAtomic(t *testing.T) {
	ctx := context.Background()
	store, mr := newOpenAICapacityQuarantineStoreForTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	opened, _, err := store.OpenOpenAICapacityQuarantine(ctx, service.OpenAICapacityQuarantineOpenRequest{
		AccountID: 21, Now: now, Cooldown: time.Minute, State: "open_initial", RuleID: "capacity",
	})
	require.NoError(t, err)
	require.True(t, opened)
	mr.FastForward(time.Minute)

	acquired, err := store.AcquireOpenAICapacityProbe(ctx, 21, "owner-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	retripAt := now.Add(time.Minute)
	opened, retrip, err := store.OpenOpenAICapacityQuarantine(ctx, service.OpenAICapacityQuarantineOpenRequest{
		AccountID: 21, Now: retripAt, Cooldown: 2 * time.Minute, State: "open_retrip", RuleID: "capacity",
	})
	require.NoError(t, err)
	require.True(t, opened)
	require.Equal(t, "open_retrip", retrip.State)
	require.Equal(t, retripAt.UnixMilli(), retrip.OpenedAt.UnixMilli())

	completed, err := store.CompleteOpenAICapacityProbe(ctx, 21, "owner-a", time.Now())
	require.NoError(t, err)
	require.False(t, completed, "a failed/retripped probe must not recover the new generation")

	releaseFirst, acquired, err := store.AcquireOpenAICapacityTripLocks(ctx, []int64{9, 7}, "trip-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = store.AcquireOpenAICapacityTripLocks(ctx, []int64{7}, "trip-b", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "overlapping group trip guards must serialize")
	releaseFirst()
	releaseSecond, acquired, err := store.AcquireOpenAICapacityTripLocks(ctx, []int64{7, 9}, "trip-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	releaseSecond()
}
