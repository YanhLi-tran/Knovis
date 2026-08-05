package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

)

// ==================== 鉴权 API（SSO 形态：只校验 Knovis JWT，不自管签发）====================
// 注册/登录/刷新由用户在 Knovis 侧完成，agent-go 不提供；保留 logout（本地黑名单）+ me（透传 Knovis）

// logout POST /auth/logout
// 将 access token 加入本地 Redis 黑名单（Redis 不可用时降级为自然过期）
// 支持从 Header 传入 token
func (s *Server) logout(c *GinCompat) {
	accessToken := ""
	if authHdr := c.GetHeader("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
		accessToken = strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
	}
	if err := s.authService.Logout(c.Request().Context(), accessToken); err != nil {
		log.Printf("[WARN][auth] 登出失败: %v", err)
	}
	// 审计：登出（userID 从 access token 解析）
	if uid := CurrentUserID(c); uid != "" {
		s.auditLogger.Log(uid, "logout", "user", uid, c.ClientIP(), c.GetString(CtxAuthType), nil)
	}
	c.JSON(http.StatusOK, H{"status": "ok", "message": "已登出"})
}

// me GET /auth/me
// 透传 Knovis 用户资料：用 token 中的 userId 调 Knovis GET /api/v1/users/:id，
// 返回 Knovis 用户资料 + Agent 侧凭证状态（has_llm_key / has_knovis_token）
func (s *Server) me(c *GinCompat) {
	uid := CurrentUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, H{"error": "未登录"})
		return
	}
	// 提取 incoming Bearer token 透传给 Knovis（Knovis 侧验签）
	token := ""
	if authHdr := c.GetHeader("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
	}
	if s.knovisClient == nil || token == "" {
		c.JSON(http.StatusServiceUnavailable, H{"error": "Knovis 客户端未配置或缺少 token"})
		return
	}
	body, err := s.knovisClient.GetUser(c.Request().Context(), token, uid)
	if err != nil {
		log.Printf("[WARN][auth] /auth/me 透传 Knovis 失败 userID=%s: %v", uid, err)
		c.JSON(http.StatusBadGateway, H{"error": "查询 Knovis 用户资料失败: " + err.Error()})
		return
	}
	// 透传 Knovis 用户资料（解析为 map 便于合并 Agent 侧状态）
	userProfile := map[string]any{}
	_ = json.Unmarshal([]byte(body), &userProfile)

	// Agent 侧凭证状态（本地凭证表，可能不存在 → 视为未设置）
	hasLLMKey, _ := s.authService.HasLLMKey(c.Request().Context(), uid)
	hasKnovisToken, _ := s.authService.HasKnovisToken(c.Request().Context(), uid)

	c.JSON(http.StatusOK, H{
		"user_id":          uid,
		"user":             userProfile,
		"has_llm_key":      hasLLMKey,
		"has_knovis_token": hasKnovisToken,
		"auth_type":        c.GetString(CtxAuthType),
	})
}

// ==================== P3: LLM key 凭证管理 API ====================

// LLMKeyRequest 设置 LLM key 请求
type LLMKeyRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

// setLLMKey POST /auth/llm-key
// 加密存储用户的 LLM key（AES-256-GCM，明文不落库）
func (s *Server) setLLMKey(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	var req LLMKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if err := s.authService.SetLLMKey(c.Request().Context(), uid, req.APIKey); err != nil {
		log.Printf("[WARN][auth] 设置 LLM key 失败 userID=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	s.auditLogger.Log(uid, "update", "llm_key", uid, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "message": "LLM key 已加密保存"})
}

// getLLMKey GET /auth/llm-key
// 返回是否已设置（不返回明文，安全考虑）
func (s *Server) getLLMKey(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	has, err := s.authService.HasLLMKey(c.Request().Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"has_llm_key": has})
}

// deleteLLMKey DELETE /auth/llm-key
// 清除用户的 LLM key
func (s *Server) deleteLLMKey(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	if err := s.authService.ClearLLMKey(c.Request().Context(), uid); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][auth] 删除用户 LLM key userID=%s", uid)
	s.auditLogger.Log(uid, "delete", "llm_key", uid, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "message": "LLM key 已删除"})
}

// ===== P4: Knovis token 凭证管理（knovis Skill 读操作用）=====

// KnovisTokenRequest Knovis token 设置请求
type KnovisTokenRequest struct {
	Token string `json:"token" binding:"required"`
}

// setKnovisToken POST /auth/knovis-token
// 加密存储用户的 Knovis token（AES-256-GCM，明文不落库）
// knovis Skill load_skill 时解密使用，作为 Authorization: Bearer <token> 透传给 Knovis
func (s *Server) setKnovisToken(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	var req KnovisTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if err := s.authService.SetKnovisToken(c.Request().Context(), uid, req.Token); err != nil {
		log.Printf("[WARN][auth] 设置 Knovis token 失败 userID=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, H{"error": err.Error()})
		return
	}
	s.auditLogger.Log(uid, "update", "knovis_token", uid, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "message": "Knovis token 已加密保存"})
}

// getKnovisToken GET /auth/knovis-token
// 返回是否已设置（不返回明文，安全考虑）
func (s *Server) getKnovisToken(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	has, err := s.authService.HasKnovisToken(c.Request().Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"has_knovis_token": has})
}

// deleteKnovisToken DELETE /auth/knovis-token
// 清除用户的 Knovis token
func (s *Server) deleteKnovisToken(c *GinCompat) {
	uid, ok := ensureUserID(c)
	if !ok {
		return
	}
	if err := s.authService.ClearKnovisToken(c.Request().Context(), uid); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][auth] 删除用户 Knovis token userID=%s", uid)
	s.auditLogger.Log(uid, "delete", "knovis_token", uid, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "message": "Knovis token 已删除"})
}
