package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// DeepSeekProvider DeepSeek LLM 实现（兼容 OpenAI 协议）
type DeepSeekProvider struct {
	apiKey   string
	baseURL  string
	model    string
	chatPath string
	client   *http.Client
}

// NewDeepSeekProvider 创建 DeepSeek provider
func NewDeepSeekProvider(apiKey, baseURL, model, chatPath string) *DeepSeekProvider {
	return &DeepSeekProvider{
		apiKey:   apiKey,
		baseURL:  strings.TrimRight(baseURL, "/"),
		model:    model,
		chatPath: chatPath,
		client: &http.Client{
			Timeout: 120 * time.Second, // 流式请求整体超时
		},
	}
}

func (p *DeepSeekProvider) APIKey() string { return p.apiKey }
func (p *DeepSeekProvider) Model() string  { return p.model }

// sseResponse DeepSeek SSE 响应结构
type sseResponse struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []rawToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// rawToolCall SSE 中的原始 tool_call（index 用于流式累积）
type rawToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatStream 流式调用 DeepSeek API
func (p *DeepSeekProvider) ChatStream(ctx context.Context, req ChatRequest) <-chan StreamChunk {
	ch := make(chan StreamChunk, 16)

	go func() {
		defer close(ch)

		req.Model = p.model
		req.Stream = true
		body, err := json.Marshal(req)
		if err != nil {
			ch <- StreamChunk{FinishReason: "error"}
			log.Printf("[llm] 请求序列化失败: %v", err)
			return
		}

		url := p.baseURL + p.chatPath
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			ch <- StreamChunk{FinishReason: "error"}
			log.Printf("[llm] 创建请求失败: %v", err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			ch <- StreamChunk{FinishReason: "error"}
			log.Printf("[llm] 请求失败: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{FinishReason: "error"}
			log.Printf("[llm] API 返回 %d: %s", resp.StatusCode, string(errBody[:min(len(errBody), 500)]))
			return
		}

		// 解析 SSE 流
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer

		// 累积 tool_calls（按 index 聚合）
		toolCallAccumulator := map[int]*ToolCall{}

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var sse sseResponse
			if err := json.Unmarshal([]byte(data), &sse); err != nil {
				continue
			}

			chunk := StreamChunk{}

			for _, choice := range sse.Choices {
				// 推送增量文本
				if choice.Delta.Content != "" {
					chunk.DeltaContent += choice.Delta.Content
				}

				// 累积 tool_calls
				for _, rtc := range choice.Delta.ToolCalls {
					tc, exists := toolCallAccumulator[rtc.Index]
					if !exists {
						tc = &ToolCall{
							ID:   rtc.ID,
							Type: "function",
							Function: FunctionCall{
								Name: rtc.Function.Name,
							},
						}
						toolCallAccumulator[rtc.Index] = tc
					}
					if rtc.ID != "" {
						tc.ID = rtc.ID
					}
					if rtc.Function.Name != "" {
						tc.Function.Name = rtc.Function.Name
					}
					tc.Function.Arguments += rtc.Function.Arguments
				}

				if choice.FinishReason != "" {
					chunk.FinishReason = choice.FinishReason
				}
			}

			if sse.Usage != nil {
				chunk.Usage = sse.Usage
			}

			// 有内容或 finish_reason 时推送
			if chunk.DeltaContent != "" || chunk.FinishReason != "" || chunk.Usage != nil {
				ch <- chunk
			}
		}

		// finish_reason=tool_calls 时，推送累积的完整 tool_calls
		if len(toolCallAccumulator) > 0 {
			finalCalls := make([]ToolCall, 0, len(toolCallAccumulator))
			for _, tc := range toolCallAccumulator {
				finalCalls = append(finalCalls, *tc)
			}
			ch <- StreamChunk{
				ToolCalls:    finalCalls,
				FinishReason: "tool_calls",
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("[llm] 流读取错误: %v", err)
		}
	}()

	return ch
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ResolveAPIKey 解析实际使用的 API key（请求头优先，兜底全局）
func ResolveAPIKey(headerKey, fallbackKey string) (string, error) {
	if headerKey != "" {
		return headerKey, nil
	}
	if fallbackKey != "" {
		return fallbackKey, nil
	}
	return "", fmt.Errorf("未提供 LLM API Key，请在请求头 X-LLM-API-Key 中传入或配置 LLM_API_KEY 环境变量")
}
