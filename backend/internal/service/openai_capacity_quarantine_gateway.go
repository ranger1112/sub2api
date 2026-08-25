package service

import "context"

// IsOpenAICapacityQuarantined is the selector-side check. Store and policy
// failures deliberately return false so a transient Redis/settings outage does
// not turn into a fleet-wide routing outage.
func (s *OpenAIGatewayService) IsOpenAICapacityQuarantined(ctx context.Context, account *Account) bool {
	if s == nil || s.openaiCapacityQuarantine == nil {
		return false
	}
	return s.openaiCapacityQuarantine.ExcludesFromScheduling(ctx, account)
}

// AcquireOpenAICapacityAdmission obtains the single half-open permit after a
// concrete account was chosen and its normal concurrency slot is held. Both
// returned callbacks are idempotent: release runs when forwarding ends, while
// completeSuccess must run only after this exact admitted request succeeds.
func (s *OpenAIGatewayService) AcquireOpenAICapacityAdmission(ctx context.Context, account *Account) (release func(), completeSuccess func(), admitted bool) {
	if s == nil || s.openaiCapacityQuarantine == nil {
		return func() {}, func() {}, true
	}
	return s.openaiCapacityQuarantine.AcquireAdmission(ctx, account)
}

func (s *OpenAIGatewayService) recordOpenAICapacityUpstreamError(ctx context.Context, account *Account, statusCode int, responseBody []byte, canonicalModel string) bool {
	if s == nil || s.openaiCapacityQuarantine == nil {
		return false
	}
	return s.openaiCapacityQuarantine.RecordUpstreamError(ctx, account, statusCode, responseBody, canonicalModel)
}
