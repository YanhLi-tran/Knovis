package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"agent-go/internal/trace"
)

// Context keys（统一管理，避免拼写错误）
const (
	CtxUserID   = "user_id"   // JWT 解析出的用户 ID（阶段三 owner_id 来源）
	CtxUsername = "username"
	CtxAuthType = "auth_type" // jwt / client_id（过渡期区分来源，便于审计）
)

// AuthMiddleware 鉴权中间件（go-zero rest 模式）
// 策略（AUTH_MODE 控制）：
//   - strict: 仅接受 JWT，无 JWT 返回 401（生产模式）
//   - dev: JWT 优先；无 JWT 时降级到 X-Client-ID（过渡期兼容老前端，默认）
//
// 白名单（不走鉴权）：/health /auth/* /static/* /  /
// 鉴权成功后注入 c.Set("user_id", ...) + c.Set("client_id", ...)
// 注：保留 client_id 注入是为了让现有 API 层 c.GetString("client_id") 零改动
//
// 全链路 trace：所有请求（含公开路径）先注入 trace_id 到 request context，
// 并回显到响应头 X-Trace-Id；下游 HTTP 调用（rag/memory/LLM）与 WS 工具执行均透传该 trace_id
func (s *Server) AuthMiddleware() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// 注入 trace_id（透传请求头 X-Trace-Id，无则新生成）并回显响应头
			r = r.WithContext(withRequestContext(r))
			w.Header().Set("X-Trace-Id", trace.TraceIDFromContext(r.Context()))

			// 白名单放行
			if isPublicPath(r.URL.Path) {
				next(w, r)
				return
			}

			userID := ""
			authType := ""

			// 1) 优先解析 JWT（Authorization: Bearer xxx）
			if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
				token := strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
				if token != "" && s.authService != nil {
					claims, err := s.authService.IsAccessTokenValid(r.Context(), token)
					if err != nil {
						log.Printf("[WARN][auth] JWT 校验失败: %v", err)
						writeErr(w, http.StatusUnauthorized, "token 无效或已过期", err.Error())
						return
					}
					userID = claims.UserIDString()
					authType = "jwt"
				}
			}

			// 2) dev 模式降级到 X-Client-ID（无 JWT 时）
			if userID == "" && s.authMode == "dev" {
				clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))
				if clientID != "" {
					userID = clientID
					authType = "client_id"
				}
			}

			// 3) 仍未识别身份
			if userID == "" {
				if s.authMode == "strict" {
					writeErr(w, http.StatusUnauthorized, "缺少鉴权信息（需要 Authorization: Bearer token）", "")
					return
				}
				// dev 模式：chat 接口允许匿名单轮（保持阶段一行为）
				// Session/Project 接口在各自 handler 里检查 client_id 为空时报 400
				next(w, r)
				return
			}

			// 注入身份到 request context
			ctx := context.WithValue(r.Context(), CtxKey(CtxUserID), userID)
			ctx = context.WithValue(ctx, CtxKey(CtxAuthType), authType)
			// 保留 client_id 注入：现有 API 层大量使用 c.GetString("client_id")，零改动兼容
			ctx = context.WithValue(ctx, CtxKey("client_id"), userID)
			next(w, r.WithContext(ctx))
		}
	}
}

// isPublicPath 判断是否为公开路径（无需鉴权）
// 注意：/auth/me 与 /auth/llm-key、/auth/knovis-token 需要鉴权（不在白名单）；仅 /auth/logout 公开
func isPublicPath(path string) bool {
	if path == "/health" || path == "/" {
		return true
	}
	// local-agent 下载引导信息公开（不含用户数据）
	if path == "/agent/local/info" {
		return true
	}
	if strings.HasPrefix(path, "/static/") {
		return true
	}
	// /ws/* WebSocket 端点自鉴权（query token），跳过 AuthMiddleware
	if strings.HasPrefix(path, "/ws/") {
		return true
	}
	// /auth/logout 公开（登出无需 token 即可调用）
	// 其余 /auth/* （me / llm-key / knovis-token）需要鉴权，不在白名单
	// /api/auth/login、/api/auth/register 公开(Knovis 登录/注册反向代理,前端同源调用)
	if path == "/api/auth/login" || path == "/api/auth/register" {
		return true
	}
	if path == "/auth/logout" {
		return true
	}
	return false
}

// CurrentUserID 从 context 取当前用户 ID（统一访问器）
// 优先取 JWT 解析的 user_id，回退到 client_id（过渡期兼容）
func CurrentUserID(c *GinCompat) string {
	if uid := c.GetString(CtxUserID); uid != "" {
		return uid
	}
	return c.GetString("client_id")
}

// ensureUserID 确保 context 中有用户身份，无则写入 401/400 响应并返回 false
// 需要鉴权的写操作 handler 调用
func ensureUserID(c *GinCompat) (string, bool) {
	uid := CurrentUserID(c)
	if uid == "" {
		// 区分 strict / dev 模式的错误提示
		if c.GetString(CtxAuthType) == "" {
			c.JSON(http.StatusUnauthorized, H{"error": "缺少鉴权信息"})
		} else {
			c.JSON(http.StatusBadRequest, H{"error": "缺少用户身份"})
		}
		return "", false
	}
	return uid, true
}

// withRequestContext 给 context 注入 trace_id（全链路追踪：透传 X-Trace-Id，无则新生成）
// 生成规则：优先复用调用方透传的 X-Trace-Id（网关/前端可带），否则生成短 uuid。
// 透传值做长度/字符收敛（≤64 字符），防止被回显进日志/请求头造成注入或膨胀。
// 下游消费方：rag/memory client（HTTP 头 X-Trace-Id）、LLM provider（请求头）、
// WS local-agent 工具执行（消息 trace_id）、SSE 事件（event.TraceID）。
func withRequestContext(r *http.Request) context.Context {
	traceID := sanitizeTraceID(r.Header.Get("X-Trace-Id"))
	if traceID == "" {
		traceID = trace.NewID()
	}
	return trace.WithTraceID(r.Context(), traceID)
}

// sanitizeTraceID 收敛外部传入的 trace_id（长度 ≤64、仅保留可打印 ASCII，防日志注入）
func sanitizeTraceID(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, c := range s {
		if c >= 0x21 && c <= 0x7e { // 可打印 ASCII（排除控制字符）
			b.WriteRune(c)
		}
	}
	return b.String()
}

// writeErr 写入错误 JSON 响应（统一格式，含可选 detail）
func writeErr(w http.ResponseWriter, code int, message, detail string) {
	body := H{"error": message}
	if detail != "" {
		body["detail"] = detail
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
