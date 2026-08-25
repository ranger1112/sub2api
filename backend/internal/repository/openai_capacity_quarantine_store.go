package repository

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAICapacityQuarantinePrefix = "sub2api:openai:capacity:"

var openAICapacityRecordEventScript = redis.NewScript(`
local events = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local event_id = ARGV[3]
redis.call('ZREMRANGEBYSCORE', events, '-inf', now - window)
local inserted = redis.call('ZADD', events, 'NX', now, event_id)
redis.call('PEXPIRE', events, window + 1000)
local count = redis.call('ZCARD', events)
return {count, inserted}
`)

var openAICapacityRecordGroupEventScript = redis.NewScript(`
local events = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local account_id = ARGV[3]
redis.call('ZREMRANGEBYSCORE', events, '-inf', now - window)
-- Keep the most recent signal for an account.  Using NX here would evict an
-- account that keeps failing before the rolling window actually expires.
redis.call('ZADD', events, now, account_id)
redis.call('PEXPIRE', events, window + 1000)
return redis.call('ZCARD', events)
`)

var openAICapacityAcquireTripLocksScript = redis.NewScript(`
local owner = ARGV[1]
local lease_ms = tonumber(ARGV[2])
for _, key in ipairs(KEYS) do
  if redis.call('EXISTS', key) == 1 then
    return 0
  end
end
for _, key in ipairs(KEYS) do
  redis.call('SET', key, owner, 'PX', lease_ms)
end
return 1
`)

var openAICapacityReleaseTripLocksScript = redis.NewScript(`
local owner = ARGV[1]
for _, key in ipairs(KEYS) do
  if redis.call('GET', key) == owner then
    redis.call('DEL', key)
  end
end
return 1
`)

var openAICapacityOpenScript = redis.NewScript(`
local open_key = KEYS[1]
local meta_key = KEYS[2]
local generation_key = KEYS[3]
local probe_key = KEYS[4]
local now = tonumber(ARGV[1])
local cooldown_ms = tonumber(ARGV[2])
local state = ARGV[3]
local rule_id = ARGV[4]
local meta_ttl_ms = tonumber(ARGV[5])
local existing = redis.call('GET', open_key)
if existing then
  return {0, tonumber(existing), redis.call('PTTL', open_key)}
end
local generation = redis.call('INCR', generation_key)
redis.call('SET', open_key, generation, 'PX', cooldown_ms)
redis.call('HSET', meta_key,
  'state', state,
  'generation', generation,
  'cooldown_until_ms', now + cooldown_ms,
  'opened_at_ms', now,
  'rule_id', rule_id,
  'owner', '',
  'updated_at_ms', now)
redis.call('DEL', probe_key)
redis.call('PEXPIRE', meta_key, meta_ttl_ms)
redis.call('PEXPIRE', generation_key, meta_ttl_ms)
return {1, generation, cooldown_ms}
`)

var openAICapacityAcquireProbeScript = redis.NewScript(`
local open_key = KEYS[1]
local meta_key = KEYS[2]
local probe_key = KEYS[3]
local now = tonumber(ARGV[1])
local owner = ARGV[2]
local lease_ms = tonumber(ARGV[3])
if redis.call('EXISTS', open_key) == 1 then
  return 0
end
local state = redis.call('HGET', meta_key, 'state')
if state == false or state == '' or state == 'closed' then
  return 2
end
local acquired = redis.call('SET', probe_key, owner, 'NX', 'PX', lease_ms)
if not acquired then
  return 0
end
redis.call('HSET', meta_key, 'state', 'half_open', 'owner', owner, 'updated_at_ms', now)
return 1
`)

var openAICapacityRenewProbeScript = redis.NewScript(`
local probe_key = KEYS[1]
if redis.call('GET', probe_key) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', probe_key, tonumber(ARGV[2]))
return 1
`)

var openAICapacityReleaseProbeScript = redis.NewScript(`
local probe_key = KEYS[1]
if redis.call('GET', probe_key) ~= ARGV[1] then
  return 0
end
redis.call('DEL', probe_key)
return 1
`)

var openAICapacityCompleteProbeScript = redis.NewScript(`
local open_key = KEYS[1]
local meta_key = KEYS[2]
local probe_key = KEYS[3]
local events_key = KEYS[4]
local owner = ARGV[1]
local now = tonumber(ARGV[2])
if redis.call('GET', probe_key) ~= owner then
  return 0
end
if redis.call('HGET', meta_key, 'state') ~= 'half_open' or redis.call('HGET', meta_key, 'owner') ~= owner then
  return 0
end
redis.call('DEL', probe_key)
redis.call('DEL', open_key)
redis.call('DEL', events_key)
redis.call('HSET', meta_key, 'state', 'closed', 'owner', '', 'cooldown_until_ms', 0, 'updated_at_ms', now)
return 1
`)

type openAICapacityQuarantineStore struct {
	rdb *redis.Client
}

func NewOpenAICapacityQuarantineStore(rdb *redis.Client) service.OpenAICapacityQuarantineStore {
	return &openAICapacityQuarantineStore{rdb: rdb}
}

func (s *openAICapacityQuarantineStore) RecordOpenAICapacityEvent(ctx context.Context, accountID int64, requestID string, now time.Time, window time.Duration) (int, bool, error) {
	if err := s.validate(accountID); err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(requestID) == "" {
		return 0, false, fmt.Errorf("OpenAI capacity request id is required")
	}
	windowMS := durationMilliseconds(window)
	values, err := openAICapacityRecordEventScript.Run(ctx, s.rdb, []string{s.eventsKey(accountID)}, now.UnixMilli(), windowMS, requestID).Int64Slice()
	if err != nil || len(values) != 2 {
		if err == nil {
			err = fmt.Errorf("unexpected OpenAI capacity event script result")
		}
		return 0, false, err
	}
	return int(values[0]), values[1] == 0, nil
}

func (s *openAICapacityQuarantineStore) RecordOpenAICapacityGroupEvent(ctx context.Context, groupID int64, model string, accountID int64, now time.Time, window time.Duration) (int, error) {
	if s == nil || s.rdb == nil || groupID <= 0 || accountID <= 0 {
		return 0, fmt.Errorf("OpenAI capacity group event store is not configured")
	}
	count, err := openAICapacityRecordGroupEventScript.Run(ctx, s.rdb, []string{s.groupEventsKey(groupID, model)}, now.UnixMilli(), durationMilliseconds(window), accountID).Int64()
	return int(count), err
}

func (s *openAICapacityQuarantineStore) AcquireOpenAICapacityTripLocks(ctx context.Context, groupIDs []int64, owner string, lease time.Duration) (func(), bool, error) {
	if s == nil || s.rdb == nil || strings.TrimSpace(owner) == "" || len(groupIDs) == 0 {
		return nil, false, fmt.Errorf("OpenAI capacity trip lock is not configured")
	}
	unique := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			return nil, false, fmt.Errorf("OpenAI capacity trip lock group id is invalid")
		}
		unique[groupID] = struct{}{}
	}
	ordered := make([]int64, 0, len(unique))
	for groupID := range unique {
		ordered = append(ordered, groupID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	keys := make([]string, 0, len(ordered))
	for _, groupID := range ordered {
		keys = append(keys, s.tripLockKey(groupID))
	}
	acquired, err := openAICapacityAcquireTripLocksScript.Run(ctx, s.rdb, keys, owner, durationMilliseconds(lease)).Int64()
	if err != nil || acquired != 1 {
		return nil, false, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if _, releaseErr := openAICapacityReleaseTripLocksScript.Run(context.Background(), s.rdb, keys, owner).Int64(); releaseErr != nil {
				// A TTL bounds lock lifetime, and no caller must be failed because
				// best-effort cleanup hit a transient Redis error.
				return
			}
		})
	}, true, nil
}

func (s *openAICapacityQuarantineStore) GetOpenAICapacityQuarantineState(ctx context.Context, accountID int64) (*service.OpenAICapacityQuarantineState, error) {
	if err := s.validate(accountID); err != nil {
		return nil, err
	}
	values, err := s.rdb.HGetAll(ctx, s.metaKey(accountID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	state := &service.OpenAICapacityQuarantineState{
		AccountID:  accountID,
		State:      values["state"],
		RuleID:     values["rule_id"],
		Owner:      values["owner"],
		Generation: parseInt64(values["generation"]),
	}
	if cooldownMS := parseInt64(values["cooldown_until_ms"]); cooldownMS > 0 {
		state.CooldownUntil = time.UnixMilli(cooldownMS)
	}
	if openedAtMS := parseInt64(values["opened_at_ms"]); openedAtMS > 0 {
		state.OpenedAt = time.UnixMilli(openedAtMS)
	}
	return state, nil
}

func (s *openAICapacityQuarantineStore) OpenOpenAICapacityQuarantine(ctx context.Context, request service.OpenAICapacityQuarantineOpenRequest) (bool, *service.OpenAICapacityQuarantineState, error) {
	if err := s.validate(request.AccountID); err != nil {
		return false, nil, err
	}
	if request.Now.IsZero() {
		request.Now = time.Now()
	}
	cooldownMS := durationMilliseconds(request.Cooldown)
	if cooldownMS <= 0 {
		return false, nil, fmt.Errorf("OpenAI capacity cooldown must be positive")
	}
	stateName := strings.TrimSpace(request.State)
	if stateName == "" {
		stateName = "open_initial"
	}
	// Retain metadata for slightly more than the longest permitted policy
	// interval, so an expired open key can enter half-open rather than silently
	// becoming a fresh closed account.
	metaTTL := int64(48 * time.Hour / time.Millisecond)
	values, err := openAICapacityOpenScript.Run(ctx, s.rdb, []string{s.openKey(request.AccountID), s.metaKey(request.AccountID), s.generationKey(request.AccountID), s.probeKey(request.AccountID)}, request.Now.UnixMilli(), cooldownMS, stateName, strings.TrimSpace(request.RuleID), metaTTL).Int64Slice()
	if err != nil || len(values) != 3 {
		if err == nil {
			err = fmt.Errorf("unexpected OpenAI capacity open script result")
		}
		return false, nil, err
	}
	if values[0] == 0 {
		state, stateErr := s.GetOpenAICapacityQuarantineState(ctx, request.AccountID)
		if stateErr != nil {
			return false, nil, stateErr
		}
		if state != nil {
			return false, state, nil
		}
	}
	until := request.Now.Add(time.Duration(values[2]) * time.Millisecond)
	return values[0] == 1, &service.OpenAICapacityQuarantineState{
		AccountID: request.AccountID, State: stateName, Generation: values[1], CooldownUntil: until, OpenedAt: request.Now, RuleID: request.RuleID,
	}, nil
}

func (s *openAICapacityQuarantineStore) AcquireOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, lease time.Duration) (bool, error) {
	if err := s.validate(accountID); err != nil {
		return false, err
	}
	if strings.TrimSpace(owner) == "" {
		return false, fmt.Errorf("OpenAI capacity probe owner is required")
	}
	result, err := openAICapacityAcquireProbeScript.Run(ctx, s.rdb, []string{s.openKey(accountID), s.metaKey(accountID), s.probeKey(accountID)}, time.Now().UnixMilli(), owner, durationMilliseconds(lease)).Int64()
	if err != nil {
		return false, err
	}
	return result == 1 || result == 2, nil
}

func (s *openAICapacityQuarantineStore) RenewOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, lease time.Duration) (bool, error) {
	if err := s.validate(accountID); err != nil {
		return false, err
	}
	result, err := openAICapacityRenewProbeScript.Run(ctx, s.rdb, []string{s.probeKey(accountID)}, owner, durationMilliseconds(lease)).Int64()
	return result == 1, err
}

func (s *openAICapacityQuarantineStore) ReleaseOpenAICapacityProbe(ctx context.Context, accountID int64, owner string) error {
	if err := s.validate(accountID); err != nil {
		return err
	}
	_, err := openAICapacityReleaseProbeScript.Run(ctx, s.rdb, []string{s.probeKey(accountID)}, owner).Int64()
	return err
}

func (s *openAICapacityQuarantineStore) CompleteOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, now time.Time) (bool, error) {
	if err := s.validate(accountID); err != nil {
		return false, err
	}
	result, err := openAICapacityCompleteProbeScript.Run(ctx, s.rdb, []string{s.openKey(accountID), s.metaKey(accountID), s.probeKey(accountID), s.eventsKey(accountID)}, owner, now.UnixMilli()).Int64()
	return result == 1, err
}

func (s *openAICapacityQuarantineStore) validate(accountID int64) error {
	if s == nil || s.rdb == nil || accountID <= 0 {
		return fmt.Errorf("OpenAI capacity quarantine store is not configured")
	}
	return nil
}

func (s *openAICapacityQuarantineStore) eventsKey(accountID int64) string {
	return fmt.Sprintf("%sevents:%d", openAICapacityQuarantinePrefix, accountID)
}
func (s *openAICapacityQuarantineStore) openKey(accountID int64) string {
	return fmt.Sprintf("%sopen:%d", openAICapacityQuarantinePrefix, accountID)
}
func (s *openAICapacityQuarantineStore) metaKey(accountID int64) string {
	return fmt.Sprintf("%smeta:%d", openAICapacityQuarantinePrefix, accountID)
}
func (s *openAICapacityQuarantineStore) probeKey(accountID int64) string {
	return fmt.Sprintf("%sprobe:%d", openAICapacityQuarantinePrefix, accountID)
}
func (s *openAICapacityQuarantineStore) generationKey(accountID int64) string {
	return fmt.Sprintf("%sgeneration:%d", openAICapacityQuarantinePrefix, accountID)
}
func (s *openAICapacityQuarantineStore) groupEventsKey(groupID int64, model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		model = "_"
	}
	return fmt.Sprintf("%sgroup-events:%d:%s", openAICapacityQuarantinePrefix, groupID, model)
}
func (s *openAICapacityQuarantineStore) tripLockKey(groupID int64) string {
	return fmt.Sprintf("%strip-lock:group:%d", openAICapacityQuarantinePrefix, groupID)
}

func durationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 1
	}
	return value.Milliseconds()
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
