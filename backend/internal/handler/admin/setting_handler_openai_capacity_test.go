package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAICapacitySettingHandlerRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *openAICapacitySettingHandlerRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (r *openAICapacitySettingHandlerRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *openAICapacitySettingHandlerRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *openAICapacitySettingHandlerRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *openAICapacitySettingHandlerRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *openAICapacitySettingHandlerRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *openAICapacitySettingHandlerRepo) Delete(context.Context, string) error { return nil }

func (r *openAICapacitySettingHandlerRepo) CompareAndSwapSetting(_ context.Context, key, expected string, expectedExists bool, value string) (bool, error) {
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

func newOpenAICapacitySettingRecorder(t *testing.T, method, body string, handler func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/settings/openai-capacity-quarantine", bytes.NewBufferString(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	handler(c)
	return recorder
}

func TestSettingHandler_OpenAICapacityQuarantinePolicyAndMatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &openAICapacitySettingHandlerRepo{}
	handler := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)

	getRecorder := newOpenAICapacitySettingRecorder(t, "GET", "", handler.GetOpenAICapacityQuarantineSettings)
	require.Equal(t, 200, getRecorder.Code)
	require.Contains(t, getRecorder.Body.String(), `"mode":"disabled"`)

	policy := service.DefaultOpenAICapacityQuarantineSettings()
	policy.Mode = service.OpenAICapacityQuarantineModeShadow
	policy.GroupPolicy = []service.OpenAICapacityGroupPolicy{{
		GroupID: 7, Enabled: true, MinRemainingAccounts: 2, MaxQuarantinedFraction: 0.5,
		GlobalSpikeDistinctAccounts: 3, GlobalSpikeWindowSeconds: 60,
	}}
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	putRecorder := newOpenAICapacitySettingRecorder(t, "PUT", string(encoded), handler.UpdateOpenAICapacityQuarantineSettings)
	require.Equal(t, 200, putRecorder.Code)
	require.Contains(t, putRecorder.Body.String(), `"revision":1`)
	require.Contains(t, putRecorder.Body.String(), `"mode":"shadow"`)

	matcherRecorder := newOpenAICapacitySettingRecorder(t, "POST", `{"http_status":503,"provider_code":"model_capacity"}`, handler.TestOpenAICapacityQuarantineMatcher)
	require.Equal(t, 200, matcherRecorder.Code)
	require.Contains(t, matcherRecorder.Body.String(), `"matched":true`)
	require.Contains(t, matcherRecorder.Body.String(), `"rule_id":"builtin-model-capacity"`)

	staleRecorder := newOpenAICapacitySettingRecorder(t, "PUT", string(encoded), handler.UpdateOpenAICapacityQuarantineSettings)
	require.Equal(t, 409, staleRecorder.Code)
	require.Contains(t, staleRecorder.Body.String(), "OPENAI_CAPACITY_QUARANTINE_REVISION_CONFLICT")
}
