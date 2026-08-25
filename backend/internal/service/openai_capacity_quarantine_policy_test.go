package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type capacityPolicySettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *capacityPolicySettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (r *capacityPolicySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}
func (r *capacityPolicySettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}
func (r *capacityPolicySettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *capacityPolicySettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *capacityPolicySettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *capacityPolicySettingRepo) Delete(context.Context, string) error { return nil }
func (r *capacityPolicySettingRepo) CompareAndSwapSetting(_ context.Context, key, expected string, expectedExists bool, value string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	current, exists := r.values[key]
	if exists != expectedExists || (exists && current != expected) {
		return false, nil
	}
	r.values[key] = value
	return true, nil
}

func TestOpenAICapacityQuarantineSettings_DefaultsAreDisabledAndVersioned(t *testing.T) {
	repo := &capacityPolicySettingRepo{}
	svc := NewSettingService(repo, nil)

	initial, err := svc.GetOpenAICapacityQuarantineSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, OpenAICapacityQuarantineModeDisabled, initial.Mode)
	require.Zero(t, initial.Revision)

	initial.Mode = OpenAICapacityQuarantineModeShadow
	updated, err := svc.SetOpenAICapacityQuarantineSettings(context.Background(), initial)
	require.NoError(t, err)
	require.EqualValues(t, 1, updated.Revision)
	require.Equal(t, OpenAICapacityQuarantineModeShadow, updated.Mode)

	stale := *initial
	stale.Mode = OpenAICapacityQuarantineModeEnforce
	_, err = svc.SetOpenAICapacityQuarantineSettings(context.Background(), &stale)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrOpenAICapacityQuarantineRevisionConflict))
}

func TestOpenAICapacityQuarantineSettings_RejectsUnsafePolicy(t *testing.T) {
	settings := DefaultOpenAICapacityQuarantineSettings()
	settings.HalfOpen.MaxRequests = 2
	require.ErrorContains(t, validateOpenAICapacityQuarantineSettings(settings), "max_requests")

	settings = DefaultOpenAICapacityQuarantineSettings()
	settings.MatchRules = append(settings.MatchRules, OpenAICapacityMatchRule{
		ID: "unsafe", Enabled: true,
		Conditions: []OpenAICapacityMatchCondition{{Source: "status_code", Operator: "equals", Value: "503"}},
	})
	require.ErrorContains(t, validateOpenAICapacityQuarantineSettings(settings), "source is invalid")
}

func TestMatchOpenAICapacityError_PreservesMandatoryExclusions(t *testing.T) {
	settings := *DefaultOpenAICapacityQuarantineSettings()
	settings.MatchRules = append(settings.MatchRules, OpenAICapacityMatchRule{
		ID: "custom-rate-limit", Enabled: true,
		Conditions: []OpenAICapacityMatchCondition{{Source: "message", Operator: "contains_ci", Value: "rate limit"}},
	})

	denied := MatchOpenAICapacityError(settings, OpenAICapacityMatcherInput{
		HTTPStatus: 429, ProviderCode: "model_capacity", Message: "rate limit",
	})
	require.False(t, denied.Matched)
	require.Equal(t, "excluded_http_status", denied.RejectedBy)

	denied = MatchOpenAICapacityError(settings, OpenAICapacityMatcherInput{
		HTTPStatus: 503, ProviderCode: "model_capacity", Message: "insufficient quota despite a custom capacity code",
	})
	require.False(t, denied.Matched)
	require.Equal(t, "excluded_failure_kind", denied.RejectedBy)

	matched := MatchOpenAICapacityError(settings, OpenAICapacityMatcherInput{ProviderCode: "model_capacity"})
	require.True(t, matched.Matched)
	require.Equal(t, "builtin-model-capacity", matched.RuleID)
}
