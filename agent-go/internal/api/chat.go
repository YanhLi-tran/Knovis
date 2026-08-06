package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"agent-go/internal/rag"

	"github.com/google/uuid"
)

// ChatRequest 对话请求
// 注：project_id 不再从请求体接收，Session 归属创建时确定且不可变，对话时只认 session.project_id
type ChatRequest struct {
	Query     string `json:"query"`
	SessionID string `json:"session_id,omitempty"` // 可选，缺省则不持久化历史
	APIKey    string `json:"api_key,omitempty"`    // 可选，优先用请求头
}

// chatStream SSE 流式对话
func (s *Server) chatStream(c *GinCompat) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}

	if req.Query == "" {
		c.JSON(http.StatusBadRequest, H{"error": "query 不能为空"})
		return
	}

	// 解析 API key 优先级：用户存储的加密 LLM key → 请求头 X-LLM-API-Key → 请求体 api_key → .env 兜底
	// 用户自带 key（存储/头/体）不限流；.env 兜底 key 受免费额度限流（子任务4实施）
	clientID := c.GetString("client_id")
	apiKey := ""
	usingOwnKey := false

	// 1) 用户存储的加密 LLM key（AES-256-GCM 解密）
	if s.authService != nil && clientID != "" {
		if stored, err := s.authService.GetLLMKey(c.Request().Context(), clientID); err == nil && stored != "" {
			apiKey = stored
			usingOwnKey = true
		}
	}
	// 2) 请求头 X-LLM-API-Key
	if apiKey == "" {
		apiKey = c.GetHeader("X-LLM-API-Key")
		if apiKey != "" {
			usingOwnKey = true
		}
	}
	// 3) 请求体 api_key
	if apiKey == "" {
		apiKey = req.APIKey
		if apiKey != "" {
			usingOwnKey = true
		}
	}
	// 4) apiKey 为空时由 orchestrator 回退 .env 兜底 key（系统 key，受免费额度限流）
	c.Set("using_own_key", usingOwnKey)

	// 如果带了 session_id，校验归属
	log.Printf("[INFO][api] chatStream 入口 sessionID=%s clientID=%s usingOwnKey=%v", req.SessionID, clientID, usingOwnKey)
	var projectID string // 从 session 解析出的 project_id（Session 归属创建时确定，不可变）
	if req.SessionID != "" && clientID != "" {
		session, err := s.repos.Session.GetByID(req.SessionID, clientID)
		if err != nil {
			log.Printf("[ERROR][api] chatStream 查询 Session 失败 sessionID=%s err=%v", req.SessionID, err)
			c.JSON(http.StatusInternalServerError, H{"error": "查询 Session 失败: " + err.Error()})
			return
		}
		if session == nil {
			log.Printf("[WARN][api] chatStream Session 不存在 sessionID=%s", req.SessionID)
			c.JSON(http.StatusNotFound, H{"error": "Session 不存在"})
			return
		}
		if session.OwnerID != clientID {
			log.Printf("[WARN][api] chatStream 越权访问 clientID=%s sessionID=%s", clientID, req.SessionID)
			c.JSON(http.StatusForbidden, H{"error": "无权操作该 Session"})
			return
		}
		projectID = session.ProjectID
	}

	// 限流检查：用户自带 key 不限；使用系统兜底 key 按免费额度限流
	// 在 SSE 头设置之前执行，超限时返回标准 JSON 429 响应
	if !usingOwnKey && clientID != "" && s.authService != nil {
		remaining, err := s.authService.CheckRateLimit(c.Request().Context(), clientID, usingOwnKey)
		if err != nil {
			log.Printf("[WARN][api] 限流拦截 userID=%s: %v", clientID, err)
			c.JSON(http.StatusTooManyRequests, H{
				"error":       err.Error(),
				"retry_after": "次日凌晨 UTC 重置",
			})
			return
		}
		if remaining >= 0 {
			c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		}
	}

	// SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Nginx 不缓冲

	// 创建可取消的 context
	ctx, cancel := context.WithCancel(c.Request().Context())
	defer cancel()

	// 生成 trace_id 并注入 context，供 rag client (X-Trace-Id header) 和 SSE 事件使用
	traceID := uuid.New().String()[:8]
	ctx = rag.WithTraceID(ctx, traceID)

	// flusher 用于实时推送 SSE
	flusher, ok := c.Writer().(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, H{"error": "Streaming unsupported"})
		return
	}

	// 显式写入状态码 + 初始注释行，触发响应头立即发送（避免客户端等待头超时）
	c.Writer().WriteHeader(http.StatusOK)
	fmt.Fprintf(c.Writer(), ": connected\n\n")
	flusher.Flush()
	log.Printf("[DEBUG][api] SSE 响应头已发送，连接已建立 traceID=%s", traceID)

	// 启动 OTACO 循环（传入 userID/client_id 与 projectID 用于记忆注入）
	log.Printf("[INFO][api] chatStream 启动 OTACO traceID=%s sessionID=%s projectID=%s clientID=%s", traceID, req.SessionID, projectID, clientID)
	eventCh := s.orchestrator.Run(ctx, req.Query, apiKey, req.SessionID, clientID, projectID)

	// 消费事件 channel，推送给前端（每个事件带 trace_id）
	for event := range eventCh {
		event.TraceID = traceID
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		fmt.Fprintf(c.Writer(), "data: %s\n\n", data)
		flusher.Flush()
		log.Printf("[DEBUG][api] SSE 已写入并 flush type=%s bytes=%d traceID=%s", event.Type, len(data), traceID)
	}

}
