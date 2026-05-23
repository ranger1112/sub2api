package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	openAIResponsesCircuitWindow             = 5 * time.Minute
	openAIResponsesCircuitCooldown           = 30 * time.Minute
	openAIResponsesCircuitConsecutiveEOFN    = 3
	openAIResponsesCircuitWindow502Threshold = 5
)

type openAIResponsesFailureCircuitBreaker struct {
	mu     sync.Mutex
	states map[int64]*openAIResponsesFailureState
}

type openAIResponsesFailureState struct {
	consecutiveEOF int
	failures       []time.Time
}

func newOpenAIResponsesFailureCircuitBreaker() *openAIResponsesFailureCircuitBreaker {
	return &openAIResponsesFailureCircuitBreaker{
		states: make(map[int64]*openAIResponsesFailureState),
	}
}

// RecordOpenAIResponsesSuccess resets the in-memory failure streak for an account after
// a successful /responses request.
func (s *OpenAIGatewayService) RecordOpenAIResponsesSuccess(accountID int64) {
	if s == nil || s.responsesFailureCircuitBreaker == nil || accountID <= 0 {
		return
	}
	s.responsesFailureCircuitBreaker.reset(accountID)
}

// RecordOpenAIResponsesForwardFailure records a non-failover /responses upstream failure.
// It automatically temp-unschedules accounts that repeatedly fail with EOF/502 patterns
// so the next retry can be routed to a healthier account.
func (s *OpenAIGatewayService) RecordOpenAIResponsesForwardFailure(ctx context.Context, accountID int64, err error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 || err == nil {
		return
	}
	if s.responsesFailureCircuitBreaker == nil {
		s.responsesFailureCircuitBreaker = newOpenAIResponsesFailureCircuitBreaker()
	}

	now := time.Now()
	decision := s.responsesFailureCircuitBreaker.recordFailure(accountID, err, now)
	if !decision.shouldTrip {
		return
	}

	until := now.Add(openAIResponsesCircuitCooldown)
	reason := fmt.Sprintf(
		"OpenAI /responses auto temp-unschedule: %s (consecutive_eof=%d, window_502=%d/%d, cooldown=%s)",
		decision.reason,
		decision.consecutiveEOF,
		decision.window502Count,
		openAIResponsesCircuitWindow502Threshold,
		openAIResponsesCircuitCooldown,
	)
	if len(reason) > 512 {
		reason = reason[:512]
	}

	if err := s.accountRepo.SetTempUnschedulable(ctx, accountID, until, reason); err != nil {
		slog.Warn("openai_responses_circuit_temp_unschedulable_failed",
			"account_id", accountID,
			"reason", decision.reason,
			"error", err,
		)
		return
	}

	slog.Warn("openai_responses_circuit_temp_unschedulable",
		"account_id", accountID,
		"until", until,
		"reason", reason,
		"consecutive_eof", decision.consecutiveEOF,
		"window_502_count", decision.window502Count,
	)
}

type openAIResponsesCircuitDecision struct {
	shouldTrip     bool
	reason         string
	consecutiveEOF int
	window502Count int
}

func (b *openAIResponsesFailureCircuitBreaker) reset(accountID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, accountID)
}

func (b *openAIResponsesFailureCircuitBreaker) recordFailure(accountID int64, err error, now time.Time) openAIResponsesCircuitDecision {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.states == nil {
		b.states = make(map[int64]*openAIResponsesFailureState)
	}
	state := b.states[accountID]
	if state == nil {
		state = &openAIResponsesFailureState{}
		b.states[accountID] = state
	}

	if isOpenAIResponsesEOFError(err) {
		state.consecutiveEOF++
	} else {
		state.consecutiveEOF = 0
	}

	cutoff := now.Add(-openAIResponsesCircuitWindow)
	failures := state.failures[:0]
	for _, ts := range state.failures {
		if ts.After(cutoff) {
			failures = append(failures, ts)
		}
	}
	state.failures = append(failures, now)
	windowCount := len(state.failures)

	decision := openAIResponsesCircuitDecision{
		consecutiveEOF: state.consecutiveEOF,
		window502Count: windowCount,
	}
	switch {
	case state.consecutiveEOF >= openAIResponsesCircuitConsecutiveEOFN:
		decision.shouldTrip = true
		decision.reason = "consecutive EOF upstream failures"
	case windowCount >= openAIResponsesCircuitWindow502Threshold:
		decision.shouldTrip = true
		decision.reason = "502 threshold exceeded within 5m"
	}
	return decision
}

func isOpenAIResponsesEOFError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof")
}
