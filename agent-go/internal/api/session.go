package api

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"time"

	"agent-go/internal/memory"
	"agent-go/internal/storage"
)

// newUUID 生成 UUID v4（避免引入 google/uuid 依赖）
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况下回退到时间戳兜底
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	// 设置 version 4 和 variant 位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CreateSessionRequest 创建 Session 请求
type CreateSessionRequest struct {
	Title     string `json:"title,omitempty"`     // 可选，缺省 "新对话"
	ProjectID string `json:"project_id,omitempty"` // 可选，归属某项目
}

// UpdateSessionRequest 更新 Session 请求（重命名 / 置顶）
// 字段为指针类型以区分 "未提供" 和 "显式置 false"
type UpdateSessionRequest struct {
	Title  *string `json:"title,omitempty"`
	Pinned *bool   `json:"pinned,omitempty"`
}

// createSession POST /sessions
// 若指定 project_id，校验该项目存在且归属当前用户（多用户防越权）
func (s *Server) createSession(c *GinCompat) {
	ownerID := c.GetString("client_id")
	if ownerID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}

	var req CreateSessionRequest
	// 允许空 body
	_ = c.ShouldBindJSON(&req)

	// 归属校验：project_id 非空时必须归属当前用户（Session 归属创建时确定，之后不可变）
	if req.ProjectID != "" {
		p, err := s.repos.Project.GetByID(req.ProjectID, ownerID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, H{"error": "查询项目失败: " + err.Error()})
			return
		}
		if p == nil {
			c.JSON(http.StatusNotFound, H{"error": "指定的 project_id 不存在"})
			return
		}
		if p.OwnerID != ownerID {
			// 防越权：不能把 session 挂到他人项目下
			log.Printf("[WARN][api] createSession 越权挂载 clientID=%s projectID=%s", ownerID, req.ProjectID)
			c.JSON(http.StatusForbidden, H{"error": "无权在该项目下创建 Session"})
			return
		}
	}

	title := req.Title
	if title == "" {
		title = "新对话"
	}

	now := time.Now().UTC()
	session := &storage.Session{
		ID:           newUUID(),
		OwnerID:      ownerID,
		ProjectID:    req.ProjectID,
		Title:        title,
		LastActiveAt: now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repos.Session.Create(session); err != nil {
		log.Printf("[ERROR][api] createSession 失败 ownerID=%s projectID=%s err=%v", ownerID, req.ProjectID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "创建 Session 失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] createSession 成功 id=%s projectID=%s ownerID=%s", session.ID, session.ProjectID, ownerID)

	s.auditLogger.Log(CurrentUserID(c), "create", "session", session.ID, c.ClientIP(), c.GetString(CtxAuthType), map[string]any{"title": session.Title, "project_id": session.ProjectID})
	c.JSON(http.StatusCreated, session)
}

// listSessions GET /sessions
func (s *Server) listSessions(c *GinCompat) {
	ownerID := c.GetString("client_id")
	if ownerID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}

	sessions, err := s.repos.Session.ListByOwner(ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询 Session 列表失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, H{
		"sessions": sessions,
		"total":    len(sessions),
	})
}

// getSession GET /sessions/:id
func (s *Server) getSession(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	session, err := s.repos.Session.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "Session 不存在"})
		return
	}

	// 权限校验：只能查自己的 Session
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] getSession 越权访问 clientID=%s sessionID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权访问该 Session"})
		return
	}

	c.JSON(http.StatusOK, session)
}

// updateSession PATCH /sessions/:id
// 支持重命名（title）和置顶（pinned），可单独或同时更新
func (s *Server) updateSession(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")

	var req UpdateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}

	// 先校验归属
	session, err := s.repos.Session.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "Session 不存在"})
		return
	}
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] updateSession 越权访问 clientID=%s sessionID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权修改该 Session"})
		return
	}

	// 按字段更新
	if req.Title != nil {
		if err := s.repos.Session.UpdateTitle(id, ownerID, *req.Title); err != nil {
			log.Printf("[ERROR][api] updateSession 重命名失败 id=%s err=%v", id, err)
			c.JSON(http.StatusInternalServerError, H{"error": "重命名失败: " + err.Error()})
			return
		}
		log.Printf("[INFO][api] updateSession 重命名成功 id=%s title=%s", id, *req.Title)
	}
	if req.Pinned != nil {
		if err := s.repos.Session.UpdatePinned(id, ownerID, *req.Pinned); err != nil {
			log.Printf("[ERROR][api] updateSession 置顶失败 id=%s err=%v", id, err)
			c.JSON(http.StatusInternalServerError, H{"error": "置顶失败: " + err.Error()})
			return
		}
		log.Printf("[INFO][api] updateSession 置顶成功 id=%s pinned=%v", id, *req.Pinned)
	}

	// 返回最新数据
	updated, _ := s.repos.Session.GetByID(id, ownerID)
	detail := map[string]any{}
	if req.Title != nil {
		detail["title"] = *req.Title
	}
	if req.Pinned != nil {
		detail["pinned"] = *req.Pinned
	}
	s.auditLogger.Log(CurrentUserID(c), "update", "session", id, c.ClientIP(), c.GetString(CtxAuthType), detail)
	c.JSON(http.StatusOK, updated)
}

// deleteSession DELETE /sessions/:id （软删除，关联消息一并软删除）
func (s *Server) deleteSession(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")

	// 先校验归属
	session, err := s.repos.Session.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "Session 不存在"})
		return
	}
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] deleteSession 越权访问 clientID=%s sessionID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权删除该 Session"})
		return
	}

	if err := s.repos.Session.SoftDelete(id, ownerID); err != nil {
		log.Printf("[ERROR][api] deleteSession 软删失败 id=%s err=%v", id, err)
		c.JSON(http.StatusInternalServerError, H{"error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] deleteSession 软删成功 id=%s", id)

	s.auditLogger.Log(CurrentUserID(c), "delete", "session", id, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "id": id})
}

// listSessionMessages GET /sessions/:id/messages
// 返回某会话的全部消息（按时间正序），用于切换会话时回放历史
func (s *Server) listSessionMessages(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")

	// 先校验归属
	session, err := s.repos.Session.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "Session 不存在"})
		return
	}
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] listSessionMessages 越权访问 clientID=%s sessionID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权访问该 Session"})
		return
	}

	msgs, err := s.repos.Message.GetBySessionID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询消息失败: " + err.Error()})
		return
	}

	// user 消息落库含动态后缀（记忆上下文/当前时间/上下文状态，KV 缓存优化：落库与
	// 发 LLM 一致保证跨请求前缀稳定）；历史回放仅展示用户原话，返回前剥离
	for i := range msgs {
		if msgs[i].Role == "user" {
			msgs[i].Content = memory.StripDynamicSuffix(msgs[i].Content)
		}
	}

	c.JSON(http.StatusOK, H{
		"messages": msgs,
		"total":    len(msgs),
	})
}
