package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

type memoryBenchmarkUpstream struct{}

func (memoryBenchmarkUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	const response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_memory\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

func (u memoryBenchmarkUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

// 使用与线上长会话同量级的图片输入，实际执行 Codex 转换和流式响应处理。
// 上游替身仅消费请求并返回固定用量，测量范围聚焦网关自身的分配。
func BenchmarkOpenAICodexLargeRequest(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, size := range []int{1 << 20, 16 << 20, 112 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			prefix := []byte(`{"model":"gpt-6.1-sol","stream":true,"instructions":"Help with this image.","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,`)
			body := make([]byte, len(prefix), len(prefix)+size+16)
			copy(body, prefix)
			body = append(body, bytes.Repeat([]byte{'A'}, size)...)
			body = append(body, []byte(`"}]}]}`)...)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: memoryBenchmarkUpstream{}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.149.0")
				result, err := svc.Forward(context.Background(), c, account, body)
				if err != nil || result == nil || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 {
					b.Fatalf("Codex 请求应完成并返回完整用量：result=%+v err=%v", result, err)
				}
			}
		})
	}
}
