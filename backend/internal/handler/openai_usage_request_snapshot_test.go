package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageSnapshotUpstream struct {
	service.HTTPUpstream
}

func (u *usageSnapshotUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"response-snapshot","object":"response","model":"gpt-5.2","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)),
	}, nil
}

func TestOpenAIResponsesQueuedUsageKeepsCompletedRequestAttribution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	account := service.Account{
		ID: 901, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.test"},
		Extra:       map[string]any{"openai_passthrough": true},
	}
	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		accountRepo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, &usageSnapshotUpstream{},
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	pool := newUsageRecordTestPool(t)
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	defer close(releaseWorker)
	pool.Submit(func(context.Context) {
		close(workerStarted)
		<-releaseWorker
	})
	<-workerStarted
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
		&service.APIKeyService{}, pool, nil, nil, nil, cfg)

	groupID := int64(701)
	apiKey := &service.APIKey{
		ID: 702, GroupID: &groupID,
		User:  &service.User{ID: 703, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	requestCtx = context.WithValue(requestCtx, ctxkey.ClientRequestID, "client-original")
	requestCtx = context.WithValue(requestCtx, ctxkey.RequestedPublicModel, "public-original")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`)).WithContext(requestCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID})

	h.Responses(c)
	require.Equal(t, http.StatusOK, rec.Code)
	cancelRequest()
	// 请求结束后，框架将上下文交给下一次请求；计量保留原请求归因。
	nextCtx := context.WithValue(context.Background(), ctxkey.RequestedPublicModel, "public-next")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(nextCtx)
	releaseWorker <- struct{}{}

	select {
	case usage := <-usageRepo.created:
		require.Equal(t, "public-original", usage.RequestedModel)
		require.Equal(t, 2, usage.InputTokens)
		require.Equal(t, 1, usage.OutputTokens)
	case <-time.After(3 * time.Second):
		t.Fatal("计量任务应在原请求结束后完成")
	}
}
