package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAICapacityQuarantineMode makes rollout explicit. A Boolean cannot safely
// express the essential shadow mode between disabled and enforce.
type OpenAICapacityQuarantineMode string

const (
	OpenAICapacityQuarantineModeDisabled OpenAICapacityQuarantineMode = "disabled"
	OpenAICapacityQuarantineModeShadow   OpenAICapacityQuarantineMode = "shadow"
	OpenAICapacityQuarantineModeEnforce  OpenAICapacityQuarantineMode = "enforce"
)

const (
	openAICapacityMinWindowSeconds          = 30
	openAICapacityMaxWindowSeconds          = 3600
	openAICapacityMinThreshold              = 2
	openAICapacityMaxThreshold              = 20
	openAICapacityMaxCooldownSeconds        = 24 * 60 * 60
	openAICapacityMinProbeLeaseSeconds      = 30
	openAICapacityMaxProbeLeaseSeconds      = 15 * 60
	openAICapacityMaxRules                  = 32
	openAICapacityMaxConditionsPerRule      = 4
	openAICapacityMaxRuleValueLength        = 256
	openAICapacityMaxGroupPolicies          = 256
	openAICapacityMaxGlobalSpikeWindowSecs  = 3600
	openAICapacityDefaultWindowSeconds      = 300
	openAICapacityDefaultInitialCooldownSec = 600
	openAICapacityDefaultRetripWindowSec    = 3600
	openAICapacityDefaultRetripCooldownSec  = 1800
	openAICapacityDefaultProbeLeaseSec      = 120
	openAICapacityDefaultProbeRenewSec      = 30
)

var ErrOpenAICapacityQuarantineRevisionConflict = infraerrors.Conflict(
	"OPENAI_CAPACITY_QUARANTINE_REVISION_CONFLICT",
	"OpenAI capacity quarantine settings were modified by another administrator; refresh and retry",
)

// OpenAICapacityQuarantineSettings is the administrator-owned policy. All time
// values are seconds so JSON, UI validation, and Redis TTLs have one unit.
type OpenAICapacityQuarantineSettings struct {
	Revision int64                        `json:"revision"`
	Mode     OpenAICapacityQuarantineMode `json:"mode"`

	WindowSeconds          int `json:"window_seconds"`
	ErrorThreshold         int `json:"error_threshold"`
	InitialCooldownSeconds int `json:"initial_cooldown_seconds"`
	RetripWindowSeconds    int `json:"retrip_window_seconds"`
	RetripCooldownSeconds  int `json:"retrip_cooldown_seconds"`
	MaxCooldownSeconds     int `json:"max_cooldown_seconds"`

	HalfOpen    OpenAICapacityHalfOpenPolicy `json:"half_open"`
	MatchRules  []OpenAICapacityMatchRule    `json:"match_rules"`
	GroupPolicy []OpenAICapacityGroupPolicy  `json:"group_policies"`
}

type OpenAICapacityHalfOpenPolicy struct {
	// MaxRequests is intentionally fixed at one for the first implementation.
	MaxRequests         int `json:"max_requests"`
	LeaseSeconds        int `json:"lease_seconds"`
	RenewIntervalSecond int `json:"renew_interval_seconds"`
}

// OpenAICapacityMatchRule requires every condition to match. Different rules
// are ORed. The small typed language avoids arbitrary regexes in a hot path.
type OpenAICapacityMatchRule struct {
	ID         string                         `json:"id"`
	Name       string                         `json:"name"`
	Enabled    bool                           `json:"enabled"`
	Conditions []OpenAICapacityMatchCondition `json:"conditions"`
}

type OpenAICapacityMatchCondition struct {
	Source   string `json:"source"`   // provider_code / provider_type / message
	Operator string `json:"operator"` // equals / contains_ci
	Value    string `json:"value"`
}

// OpenAICapacityGroupPolicy controls where this account-level feature is
// enabled and the floor that must remain after a trip.
type OpenAICapacityGroupPolicy struct {
	GroupID                     int64   `json:"group_id"`
	Enabled                     bool    `json:"enabled"`
	MinRemainingAccounts        int     `json:"min_remaining_accounts"`
	MaxQuarantinedFraction      float64 `json:"max_quarantined_fraction"`
	GlobalSpikeDistinctAccounts int     `json:"global_spike_distinct_accounts"`
	GlobalSpikeWindowSeconds    int     `json:"global_spike_window_seconds"`
}

// OpenAICapacityMatcherInput contains only normalized and redacted fields. It
// is deliberately also used by the admin test endpoint, never raw upstream data.
type OpenAICapacityMatcherInput struct {
	HTTPStatus   int    `json:"http_status"`
	ProviderCode string `json:"provider_code"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
}

type OpenAICapacityMatcherResult struct {
	Matched    bool   `json:"matched"`
	RuleID     string `json:"rule_id,omitempty"`
	RejectedBy string `json:"rejected_by,omitempty"`
}

// capacityPolicyCASRepository is optional so existing in-memory test stores and
// third-party SettingRepository implementations remain source compatible.
type capacityPolicyCASRepository interface {
	CompareAndSwapSetting(ctx context.Context, key string, expectedValue string, expectedExists bool, value string) (bool, error)
}

func DefaultOpenAICapacityQuarantineSettings() *OpenAICapacityQuarantineSettings {
	return &OpenAICapacityQuarantineSettings{
		Revision:               0,
		Mode:                   OpenAICapacityQuarantineModeDisabled,
		WindowSeconds:          openAICapacityDefaultWindowSeconds,
		ErrorThreshold:         3,
		InitialCooldownSeconds: openAICapacityDefaultInitialCooldownSec,
		RetripWindowSeconds:    openAICapacityDefaultRetripWindowSec,
		RetripCooldownSeconds:  openAICapacityDefaultRetripCooldownSec,
		MaxCooldownSeconds:     openAICapacityDefaultRetripCooldownSec,
		HalfOpen: OpenAICapacityHalfOpenPolicy{
			MaxRequests:         1,
			LeaseSeconds:        openAICapacityDefaultProbeLeaseSec,
			RenewIntervalSecond: openAICapacityDefaultProbeRenewSec,
		},
		MatchRules: []OpenAICapacityMatchRule{
			{
				ID: "builtin-model-capacity", Name: "OpenAI model_capacity", Enabled: true,
				Conditions: []OpenAICapacityMatchCondition{{Source: "provider_code", Operator: "equals", Value: "model_capacity"}},
			},
			{
				ID: "builtin-server-overloaded", Name: "OpenAI overloaded message", Enabled: true,
				Conditions: []OpenAICapacityMatchCondition{{Source: "message", Operator: "contains_ci", Value: "servers are currently overloaded"}},
			},
		},
		GroupPolicy: []OpenAICapacityGroupPolicy{},
	}
}

func (s *SettingService) GetOpenAICapacityQuarantineSettings(ctx context.Context) (*OpenAICapacityQuarantineSettings, error) {
	settings, _, _, err := s.readOpenAICapacityQuarantineSettings(ctx)
	return settings, err
}

func (s *SettingService) readOpenAICapacityQuarantineSettings(ctx context.Context) (*OpenAICapacityQuarantineSettings, string, bool, error) {
	if s == nil || s.settingRepo == nil {
		return nil, "", false, fmt.Errorf("OpenAI capacity quarantine settings repository is not configured")
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICapacityQuarantineSettings)
	if errors.Is(err, ErrSettingNotFound) || strings.TrimSpace(raw) == "" {
		return DefaultOpenAICapacityQuarantineSettings(), "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("get OpenAI capacity quarantine settings: %w", err)
	}
	settings := DefaultOpenAICapacityQuarantineSettings()
	if err := json.Unmarshal([]byte(raw), settings); err != nil {
		// A malformed old value must fail safe: disabled, with a recoverable admin
		// response instead of silently enforcing an unknown policy.
		return DefaultOpenAICapacityQuarantineSettings(), raw, true, nil
	}
	if err := validateOpenAICapacityQuarantineSettings(settings); err != nil {
		return DefaultOpenAICapacityQuarantineSettings(), raw, true, nil
	}
	return settings, raw, true, nil
}

func (s *SettingService) SetOpenAICapacityQuarantineSettings(ctx context.Context, requested *OpenAICapacityQuarantineSettings) (*OpenAICapacityQuarantineSettings, error) {
	if requested == nil {
		return nil, infraerrors.BadRequest("OPENAI_CAPACITY_QUARANTINE_INVALID", "settings cannot be nil")
	}
	if err := validateOpenAICapacityQuarantineSettings(requested); err != nil {
		return nil, infraerrors.BadRequest("OPENAI_CAPACITY_QUARANTINE_INVALID", err.Error())
	}

	s.openAICapacityQuarantineSettingsMu.Lock()
	defer s.openAICapacityQuarantineSettingsMu.Unlock()

	current, raw, exists, err := s.readOpenAICapacityQuarantineSettings(ctx)
	if err != nil {
		return nil, err
	}
	if requested.Revision != current.Revision {
		return nil, ErrOpenAICapacityQuarantineRevisionConflict
	}

	updated := *requested
	updated.Revision = current.Revision + 1
	encoded, err := json.Marshal(&updated)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI capacity quarantine settings: %w", err)
	}
	if cas, ok := s.settingRepo.(capacityPolicyCASRepository); ok {
		applied, err := cas.CompareAndSwapSetting(ctx, SettingKeyOpenAICapacityQuarantineSettings, raw, exists, string(encoded))
		if err != nil {
			return nil, fmt.Errorf("persist OpenAI capacity quarantine settings: %w", err)
		}
		if !applied {
			return nil, ErrOpenAICapacityQuarantineRevisionConflict
		}
	} else if err := s.settingRepo.Set(ctx, SettingKeyOpenAICapacityQuarantineSettings, string(encoded)); err != nil {
		return nil, fmt.Errorf("persist OpenAI capacity quarantine settings: %w", err)
	}
	return &updated, nil
}

func validateOpenAICapacityQuarantineSettings(settings *OpenAICapacityQuarantineSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	switch settings.Mode {
	case OpenAICapacityQuarantineModeDisabled, OpenAICapacityQuarantineModeShadow, OpenAICapacityQuarantineModeEnforce:
	default:
		return fmt.Errorf("mode must be disabled, shadow, or enforce")
	}
	if settings.WindowSeconds < openAICapacityMinWindowSeconds || settings.WindowSeconds > openAICapacityMaxWindowSeconds {
		return fmt.Errorf("window_seconds must be between %d-%d", openAICapacityMinWindowSeconds, openAICapacityMaxWindowSeconds)
	}
	if settings.ErrorThreshold < openAICapacityMinThreshold || settings.ErrorThreshold > openAICapacityMaxThreshold {
		return fmt.Errorf("error_threshold must be between %d-%d", openAICapacityMinThreshold, openAICapacityMaxThreshold)
	}
	if settings.InitialCooldownSeconds <= 0 || settings.InitialCooldownSeconds > openAICapacityMaxCooldownSeconds {
		return fmt.Errorf("initial_cooldown_seconds must be between 1-%d", openAICapacityMaxCooldownSeconds)
	}
	if settings.RetripWindowSeconds < settings.WindowSeconds || settings.RetripWindowSeconds > openAICapacityMaxCooldownSeconds {
		return fmt.Errorf("retrip_window_seconds must be between window_seconds-%d", openAICapacityMaxCooldownSeconds)
	}
	if settings.RetripCooldownSeconds < settings.InitialCooldownSeconds || settings.RetripCooldownSeconds > openAICapacityMaxCooldownSeconds {
		return fmt.Errorf("retrip_cooldown_seconds must be between initial_cooldown_seconds-%d", openAICapacityMaxCooldownSeconds)
	}
	if settings.MaxCooldownSeconds < settings.RetripCooldownSeconds || settings.MaxCooldownSeconds > openAICapacityMaxCooldownSeconds {
		return fmt.Errorf("max_cooldown_seconds must be between retrip_cooldown_seconds-%d", openAICapacityMaxCooldownSeconds)
	}
	if settings.HalfOpen.MaxRequests != 1 {
		return fmt.Errorf("half_open.max_requests must be 1")
	}
	if settings.HalfOpen.LeaseSeconds < openAICapacityMinProbeLeaseSeconds || settings.HalfOpen.LeaseSeconds > openAICapacityMaxProbeLeaseSeconds {
		return fmt.Errorf("half_open.lease_seconds must be between %d-%d", openAICapacityMinProbeLeaseSeconds, openAICapacityMaxProbeLeaseSeconds)
	}
	if settings.HalfOpen.RenewIntervalSecond <= 0 || settings.HalfOpen.RenewIntervalSecond >= settings.HalfOpen.LeaseSeconds {
		return fmt.Errorf("half_open.renew_interval_seconds must be positive and less than lease_seconds")
	}
	if len(settings.MatchRules) > openAICapacityMaxRules {
		return fmt.Errorf("match_rules must contain at most %d rules", openAICapacityMaxRules)
	}
	seenRules := make(map[string]struct{}, len(settings.MatchRules))
	for i := range settings.MatchRules {
		rule := settings.MatchRules[i]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			return fmt.Errorf("match_rules[%d].id is required", i)
		}
		if _, exists := seenRules[rule.ID]; exists {
			return fmt.Errorf("match_rules contains duplicate id %q", rule.ID)
		}
		seenRules[rule.ID] = struct{}{}
		if len(rule.Conditions) == 0 || len(rule.Conditions) > openAICapacityMaxConditionsPerRule {
			return fmt.Errorf("match_rules[%d].conditions must contain 1-%d conditions", i, openAICapacityMaxConditionsPerRule)
		}
		for j, condition := range rule.Conditions {
			if condition.Source != "provider_code" && condition.Source != "provider_type" && condition.Source != "message" {
				return fmt.Errorf("match_rules[%d].conditions[%d].source is invalid", i, j)
			}
			if condition.Operator != "equals" && condition.Operator != "contains_ci" {
				return fmt.Errorf("match_rules[%d].conditions[%d].operator is invalid", i, j)
			}
			if value := strings.TrimSpace(condition.Value); value == "" || len(value) > openAICapacityMaxRuleValueLength {
				return fmt.Errorf("match_rules[%d].conditions[%d].value must contain 1-%d characters", i, j, openAICapacityMaxRuleValueLength)
			}
		}
	}
	if len(settings.GroupPolicy) > openAICapacityMaxGroupPolicies {
		return fmt.Errorf("group_policies must contain at most %d entries", openAICapacityMaxGroupPolicies)
	}
	seenGroups := make(map[int64]struct{}, len(settings.GroupPolicy))
	for i, group := range settings.GroupPolicy {
		if group.GroupID <= 0 {
			return fmt.Errorf("group_policies[%d].group_id must be positive", i)
		}
		if _, exists := seenGroups[group.GroupID]; exists {
			return fmt.Errorf("group_policies contains duplicate group_id %d", group.GroupID)
		}
		seenGroups[group.GroupID] = struct{}{}
		if group.MinRemainingAccounts < 1 {
			return fmt.Errorf("group_policies[%d].min_remaining_accounts must be at least 1", i)
		}
		if group.MaxQuarantinedFraction <= 0 || group.MaxQuarantinedFraction > 1 {
			return fmt.Errorf("group_policies[%d].max_quarantined_fraction must be in (0,1]", i)
		}
		if group.GlobalSpikeDistinctAccounts < 1 {
			return fmt.Errorf("group_policies[%d].global_spike_distinct_accounts must be at least 1", i)
		}
		if group.GlobalSpikeWindowSeconds < 1 || group.GlobalSpikeWindowSeconds > openAICapacityMaxGlobalSpikeWindowSecs {
			return fmt.Errorf("group_policies[%d].global_spike_window_seconds must be between 1-%d", i, openAICapacityMaxGlobalSpikeWindowSecs)
		}
	}
	return nil
}

// MatchOpenAICapacityError has a non-bypassable deny list. Custom rules can add
// capacity signatures but cannot classify authentication, quota, rate-limit, or
// local transport failures as capacity events.
func MatchOpenAICapacityError(settings OpenAICapacityQuarantineSettings, input OpenAICapacityMatcherInput) OpenAICapacityMatcherResult {
	if input.HTTPStatus == 401 || input.HTTPStatus == 403 || input.HTTPStatus == 429 {
		return OpenAICapacityMatcherResult{RejectedBy: "excluded_http_status"}
	}
	providerType := strings.ToLower(strings.TrimSpace(input.ProviderType))
	providerCode := strings.ToLower(strings.TrimSpace(input.ProviderCode))
	message := strings.ToLower(strings.TrimSpace(input.Message))
	if openAICapacityContainsAny(providerType+" "+providerCode+" "+message,
		"rate_limit", "rate limit", "quota", "insufficient_quota", "authentication", "unauthorized", "forbidden", "unexpected eof", "client cancel") {
		return OpenAICapacityMatcherResult{RejectedBy: "excluded_failure_kind"}
	}
	for _, rule := range settings.MatchRules {
		if !rule.Enabled || !openAICapacityRuleMatches(rule, providerCode, providerType, message) {
			continue
		}
		return OpenAICapacityMatcherResult{Matched: true, RuleID: rule.ID}
	}
	return OpenAICapacityMatcherResult{}
}

func openAICapacityRuleMatches(rule OpenAICapacityMatchRule, providerCode, providerType, message string) bool {
	if len(rule.Conditions) == 0 {
		return false
	}
	for _, condition := range rule.Conditions {
		var candidate string
		switch condition.Source {
		case "provider_code":
			candidate = providerCode
		case "provider_type":
			candidate = providerType
		case "message":
			candidate = message
		default:
			return false
		}
		want := strings.ToLower(strings.TrimSpace(condition.Value))
		switch condition.Operator {
		case "equals":
			if candidate != want {
				return false
			}
		case "contains_ci":
			if !strings.Contains(candidate, want) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func openAICapacityContainsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
