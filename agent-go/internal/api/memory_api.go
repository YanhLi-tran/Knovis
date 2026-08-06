package api

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"agent-go/internal/storage"

	"gorm.io/gorm"
)

// ==================== 项目管理 ====================

// CreateProjectRequest 创建项目
type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Rules       string `json:"rules,omitempty"`
	Context     string `json:"context,omitempty"`
	UserDefined string `json:"user_defined,omitempty"`
}

// UpdateProjectRequest 更新项目
type UpdateProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Rules       *string `json:"rules,omitempty"`
	Context     *string `json:"context,omitempty"`
	KeyPoints   *string `json:"key_points,omitempty"`
	UserDefined *string `json:"user_defined,omitempty"`
}

// createProject POST /projects
func (s *Server) createProject(c *GinCompat) {
	ownerID := c.GetString("client_id")
	if ownerID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, H{"error": "name 不能为空"})
		return
	}
	now := time.Now().UTC()
	p := &storage.Project{
		ID:           newUUID(),
		OwnerID:      ownerID,
		Name:         req.Name,
		Description:  req.Description,
		Rules:        req.Rules,
		Context:      req.Context,
		UserDefined:  req.UserDefined,
		LastActiveAt: now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repos.Project.Create(p); err != nil {
		log.Printf("[ERROR][api] createProject 失败 ownerID=%s name=%s err=%v", ownerID, req.Name, err)
		c.JSON(http.StatusInternalServerError, H{"error": "创建项目失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] createProject 成功 id=%s name=%s ownerID=%s", p.ID, p.Name, ownerID)
	s.auditLogger.Log(CurrentUserID(c), "create", "project", p.ID, c.ClientIP(), c.GetString(CtxAuthType), map[string]any{"name": p.Name})
	c.JSON(http.StatusCreated, p)
}

// listProjects GET /projects
func (s *Server) listProjects(c *GinCompat) {
	ownerID := c.GetString("client_id")
	if ownerID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}
	projects, err := s.repos.Project.ListByOwner(ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询项目失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"projects": projects, "total": len(projects)})
}

// getProject GET /projects/:id
func (s *Server) getProject(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	p, err := s.repos.Project.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, H{"error": "项目不存在"})
		return
	}
	if ownerID != "" && p.OwnerID != ownerID {
		log.Printf("[WARN][api] getProject 越权访问 clientID=%s projectID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权访问该项目"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// updateProject PATCH /projects/:id
func (s *Server) updateProject(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	p, err := s.repos.Project.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, H{"error": "项目不存在"})
		return
	}
	if ownerID != "" && p.OwnerID != ownerID {
		log.Printf("[WARN][api] updateProject 越权访问 clientID=%s projectID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权修改该项目"})
		return
	}
	var req UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.Description != nil {
		fields["description"] = *req.Description
	}
	if req.Rules != nil {
		fields["rules"] = *req.Rules
	}
	if req.Context != nil {
		fields["context"] = *req.Context
	}
	if req.KeyPoints != nil {
		fields["key_points"] = *req.KeyPoints
	}
	if req.UserDefined != nil {
		fields["user_defined"] = *req.UserDefined
	}
	if len(fields) > 0 {
		if err := s.repos.Project.UpdateFields(id, ownerID, fields); err != nil {
			c.JSON(http.StatusInternalServerError, H{"error": "更新失败: " + err.Error()})
			return
		}
		log.Printf("[INFO][api] updateProject 成功 id=%s fields=%v", id, fields)
		// 失效缓存
		if s.memorySvc != nil {
			s.memorySvc.InvalidateProjectCache(c.Request().Context(), id)
		}
	}
	updated, _ := s.repos.Project.GetByID(id, ownerID)
	s.auditLogger.Log(CurrentUserID(c), "update", "project", id, c.ClientIP(), c.GetString(CtxAuthType), fields)
	c.JSON(http.StatusOK, updated)
}

// deleteProject DELETE /projects/:id
// 事务级联软删：项目 + 子 session + 关联 messages + 项目记忆（3步原子）
// Chroma collection 删除与缓存失效在事务外执行（失败不阻断，最终一致）
func (s *Server) deleteProject(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	p, err := s.repos.Project.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, H{"error": "项目不存在"})
		return
	}
	if ownerID != "" && p.OwnerID != ownerID {
		log.Printf("[WARN][api] deleteProject 越权访问 clientID=%s projectID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权删除该项目"})
		return
	}

	// 事务级联软删：项目 → 子 session → 关联 messages → 项目记忆
	// 级联顺序：先删子表（messages 依赖 session，session 依赖 project），最后删 project
	if err := s.repos.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 1) 软删除项目下所有关联 messages（通过子查询匹配 project_id 下的 session_id）
		if err := tx.Where("session_id IN (?)",
			tx.Model(&storage.Session{}).Select("id").Where("project_id = ?", id),
		).Delete(&storage.Message{}).Error; err != nil {
			return err
		}
		// 2) 软删除项目下所有子 session
		if err := tx.Model(&storage.Session{}).Where("project_id = ?", id).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		// 3) 软删除项目下所有记忆
		if err := tx.Model(&storage.Memory{}).Where("project_id = ?", id).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		// 4) 软删除项目本身
		if err := tx.Model(&storage.Project{}).Where("id = ?", id).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Printf("[ERROR][api] deleteProject 级联删除失败 projectID=%s err=%v", id, err)
		c.JSON(http.StatusInternalServerError, H{"error": "级联删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] deleteProject 级联删除成功 id=%s", id)

	// Chroma collection 删除（事务外，失败不阻断，仅 log）
	if s.memorySvc != nil {
		if err := s.memorySvc.GetClient().DeleteCollection(c.Request().Context(), id); err != nil {
			// 不阻断删除流程，向量库残留可由后续 GC 清理
			log.Printf("[memory] 删除 Chroma collection 失败（项目已软删，向量残留）: %v", err)
		}
		s.memorySvc.InvalidateProjectCache(c.Request().Context(), id)
	}
	s.auditLogger.Log(CurrentUserID(c), "delete", "project", id, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "id": id})
}

// ==================== 用户档案 ====================

// UpsertUserConfigRequest 新建/更新用户档案
type UpsertUserConfigRequest struct {
	BasicInfo   string `json:"basic_info,omitempty"`
	Location    string `json:"location,omitempty"`
	Preferences string `json:"preferences,omitempty"`
	RawText     string `json:"raw_text,omitempty"` // 可选，便于直接注入
	// P9: Agent 行为设置
	MaxToolRounds int    `json:"max_tool_rounds,omitempty"` // 连续工具轮上限（0=全局默认10）
	SandboxMode   string `json:"sandbox_mode,omitempty"`    // ask/auto/yolo
	BackupMode    string `json:"backup_mode,omitempty"`     // snapshot/git
}

// upsertUserConfig PUT /memory/user-config
func (s *Server) upsertUserConfig(c *GinCompat) {
	userID := c.GetString("client_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}
	var req UpsertUserConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	uc := &storage.UserConfig{
		UserID:        userID,
		BasicInfo:     req.BasicInfo,
		Location:      req.Location,
		Preferences:   req.Preferences,
		RawText:       req.RawText,
		MaxToolRounds: req.MaxToolRounds,
		SandboxMode:   req.SandboxMode,
		BackupMode:    req.BackupMode,
	}
	if err := s.repos.UserConfig.Upsert(uc); err != nil {
		log.Printf("[ERROR][api] upsertUserConfig 失败 userID=%s err=%v", userID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] upsertUserConfig 成功 userID=%s", userID)
	// 失效缓存
	if s.repos.Cache != nil {
		_ = s.repos.Cache.Del(c.Request().Context(), storage.UserConfigCacheKey(userID))
	}
	s.auditLogger.Log(CurrentUserID(c), "update", "user_config", userID, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, uc)
}

// getUserConfig GET /memory/user-config
func (s *Server) getUserConfig(c *GinCompat) {
	userID := c.GetString("client_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 X-Client-ID"})
		return
	}
	uc, err := s.repos.UserConfig.GetByUserID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if uc == nil {
		c.JSON(http.StatusOK, H{"user_config": nil})
		return
	}
	c.JSON(http.StatusOK, H{"user_config": uc})
}

// ==================== 记忆 CRUD ====================

// CreateMemoryRequest 创建记忆
type CreateMemoryRequest struct {
	Content    string `json:"content"`
	MemoryType string `json:"memory_type,omitempty"`
	Source     string `json:"source,omitempty"`     // 默认 manual
	Importance int    `json:"importance,omitempty"`  // 默认 50
}

// createMemory POST /projects/:id/memories
func (s *Server) createMemory(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	// 校验项目归属
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	var req CreateMemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, H{"error": "content 不能为空"})
		return
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}
	importance := req.Importance
	if importance == 0 {
		importance = 50
	}
	m := &storage.Memory{
		ID:              newUUID(),
		ProjectID:       projectID,
		OwnerID:         ownerID,
		Content:         req.Content,
		MemoryType:      req.MemoryType,
		Source:          source,
		Importance:      importance,
		EmbeddingStatus: "pending",
	}
	if err := s.repos.Memory.Create(m); err != nil {
		log.Printf("[ERROR][api] createMemory 失败 projectID=%s ownerID=%s err=%v", projectID, ownerID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "创建失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] createMemory 成功 id=%s projectID=%s", m.ID, projectID)
	s.memorySvc.InvalidateProjectCache(c.Request().Context(), projectID)
	s.auditLogger.Log(CurrentUserID(c), "create", "memory", m.ID, c.ClientIP(), c.GetString(CtxAuthType), map[string]any{"project_id": m.ProjectID})
	c.JSON(http.StatusCreated, m)
}

// listMemories GET /projects/:id/memories?limit=
func (s *Server) listMemories(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	limit := 100
	// 简单解析 limit（不引入额外依赖）
	if l := c.Query("limit"); l != "" {
		n := 0
		for _, ch := range l {
			if ch < '0' || ch > '9' {
				n = 0
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n > 0 && n <= 500 {
			limit = n
		}
	}
	memories, err := s.repos.Memory.ListByProject(projectID, ownerID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"memories": memories, "total": len(memories)})
}

// updateMemory PUT /memory/memories/:id
func (s *Server) updateMemory(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	m, err := s.repos.Memory.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, H{"error": "记忆不存在"})
		return
	}
	if ownerID != "" && m.OwnerID != ownerID {
		log.Printf("[WARN][api] updateMemory 越权访问 clientID=%s memoryID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权修改该记忆"})
		return
	}
	var req struct {
		Content    *string `json:"content,omitempty"`
		Importance *int    `json:"importance,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if req.Content != nil {
		if err := s.repos.Memory.UpdateContent(id, ownerID, *req.Content); err != nil {
			log.Printf("[ERROR][api] updateMemory 更新内容失败 id=%s err=%v", id, err)
			c.JSON(http.StatusInternalServerError, H{"error": "更新失败: " + err.Error()})
			return
		}
		log.Printf("[INFO][api] updateMemory 成功 id=%s", id)
		// content 改动后需重新 embed：删除旧向量 + 标记 pending（下次达阈值时重新生成）
		if s.memorySvc != nil {
			_, _ = s.memorySvc.GetClient().Delete(c.Request().Context(), m.ProjectID, []string{id})
		}
		_ = s.repos.Memory.UpdateFields(id, ownerID, map[string]any{"embedding_status": "pending"})
		// 异步检查 embed 阈值，达阈值则批量重新 embed
		if s.memorySvc != nil {
			s.memorySvc.MaybeEmbedPending(c.Request().Context(), m.ProjectID, ownerID)
		}
	}
	if req.Importance != nil {
		_ = s.repos.Memory.UpdateFields(id, ownerID, map[string]any{"importance": *req.Importance})
	}
	s.memorySvc.InvalidateProjectCache(c.Request().Context(), m.ProjectID)
	updated, _ := s.repos.Memory.GetByID(id, ownerID)
	detail := map[string]any{}
	if req.Content != nil {
		detail["content"] = *req.Content
	}
	if req.Importance != nil {
		detail["importance"] = *req.Importance
	}
	s.auditLogger.Log(CurrentUserID(c), "update", "memory", id, c.ClientIP(), c.GetString(CtxAuthType), detail)
	c.JSON(http.StatusOK, updated)
}

// deleteMemory DELETE /memory/memories/:id
func (s *Server) deleteMemory(c *GinCompat) {
	id := c.Param("id")
	ownerID := c.GetString("client_id")
	m, err := s.repos.Memory.GetByID(id, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, H{"error": "记忆不存在"})
		return
	}
	if ownerID != "" && m.OwnerID != ownerID {
		log.Printf("[WARN][api] deleteMemory 越权访问 clientID=%s memoryID=%s", ownerID, id)
		c.JSON(http.StatusForbidden, H{"error": "无权删除该记忆"})
		return
	}
	if err := s.repos.Memory.SoftDelete(id, ownerID); err != nil {
		log.Printf("[ERROR][api] deleteMemory 软删失败 id=%s err=%v", id, err)
		c.JSON(http.StatusInternalServerError, H{"error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] deleteMemory 软删成功 id=%s", id)
	// 删除 Chroma 向量
	if s.memorySvc != nil {
		_, _ = s.memorySvc.GetClient().Delete(c.Request().Context(), m.ProjectID, []string{id})
		s.memorySvc.InvalidateProjectCache(c.Request().Context(), m.ProjectID)
	}
	s.auditLogger.Log(CurrentUserID(c), "delete", "memory", id, c.ClientIP(), c.GetString(CtxAuthType), nil)
	c.JSON(http.StatusOK, H{"status": "ok", "id": id})
}

// ==================== Embed 与检索（手动触发/测试）====================

// embedPending POST /projects/:id/memories/embed
// 手动触发批量 embed pending 记忆
func (s *Server) embedPending(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	if s.memorySvc == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "记忆服务未启用"})
		return
	}
	n, err := s.memorySvc.EmbedPendingMemories(c.Request().Context(), projectID, ownerID)
	if err != nil {
		log.Printf("[ERROR][api] embedPending 失败 projectID=%s embedded=%d err=%v", projectID, n, err)
		c.JSON(http.StatusInternalServerError, H{"error": "embedding 失败: " + err.Error(), "embedded": n})
		return
	}
	log.Printf("[INFO][api] embedPending 成功 projectID=%s count=%d", projectID, n)
	c.JSON(http.StatusOK, H{"embedded": n})
}

// searchMemories POST /projects/:id/memories/search
// 测试检索（前端调试/设置页预览用）
func (s *Server) searchMemories(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	var req struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "请求格式错误: " + err.Error()})
		return
	}
	if req.Query == "" {
		c.JSON(http.StatusBadRequest, H{"error": "query 不能为空"})
		return
	}
	if s.memorySvc == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "记忆服务未启用"})
		return
	}
	resp, err := s.memorySvc.GetClient().SearchWithStats(c.Request().Context(), projectID, req.Query, req.TopK)
	if err != nil {
		log.Printf("[ERROR][api] searchMemories 失败 projectID=%s err=%v", projectID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "检索失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] searchMemories 成功 projectID=%s bm25_count=%d rag_count=%d", projectID, resp.BM25Count, resp.RAGCount)
	c.JSON(http.StatusOK, H{
		"results":     resp.Results,
		"total":       len(resp.Results),
		"bm25_count":  resp.BM25Count,
		"rag_count":   resp.RAGCount,
	})
}

// ==================== TTL 归档（单条记忆级）====================

// archiveMemory POST /memory/memories/:id/archive
// 归档单条记忆：事务内迁移到 agent_memory_archive + 硬删主表；事务外删 Chroma 向量
// 归档后 30 天内可恢复
// 注：路由放在 /memory/memories/:id 下（与 update/delete 同层级），避免与 /projects/:id/memories/embed 等静态段冲突
func (s *Server) archiveMemory(c *GinCompat) {
	memID := c.Param("id")
	ownerID := c.GetString("client_id")

	// 查记忆（校验归属）
	m, err := s.repos.Memory.GetByID(memID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询记忆失败: " + err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, H{"error": "记忆不存在"})
		return
	}
	if ownerID != "" && m.OwnerID != ownerID {
		log.Printf("[WARN][api] archiveMemory 越权访问 clientID=%s memoryID=%s", ownerID, memID)
		c.JSON(http.StatusForbidden, H{"error": "无权归档该记忆"})
		return
	}
	projectID := m.ProjectID

	// 事务：写 archive 表 + 硬删主表
	now := time.Now().UTC()
	archive := &storage.MemoryArchive{
		ID:                m.ID,
		OriginalProjectID: m.ProjectID,
		OriginalOwnerID:   m.OwnerID,
		Content:           m.Content,
		MemoryType:        m.MemoryType,
		Source:            m.Source,
		ArchivedAt:        now,
		RestoreExpiresAt:  now.Add(30 * 24 * time.Hour), // 30 天可恢复
		CreatedAt:         m.CreatedAt,
	}
	if err := s.repos.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(archive).Error; err != nil {
			return err
		}
		// 硬删除主表记录（unscoped）
		if err := tx.Unscoped().Where("id = ?", memID).Delete(&storage.Memory{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Printf("[ERROR][api] archiveMemory 归档事务失败 id=%s err=%v", memID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "归档失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] archiveMemory 归档事务成功 id=%s", memID)

	// 事务外：删 Chroma 向量（失败不阻断，仅 log）
	if s.memorySvc != nil {
		if _, err := s.memorySvc.GetClient().Delete(c.Request().Context(), projectID, []string{memID}); err != nil {
			log.Printf("[memory] 归档后删除 Chroma 向量失败（记忆已归档，向量残留）: %v", err)
		}
		s.memorySvc.InvalidateProjectCache(c.Request().Context(), projectID)
	}
	c.JSON(http.StatusOK, H{"status": "archived", "id": memID, "restore_expires_at": archive.RestoreExpiresAt})
}

// restoreMemory POST /memory/archive/:id/restore
// 恢复归档记忆（30 天内有效）：写回主表（embedding_status=pending）+ 标记已恢复
func (s *Server) restoreMemory(c *GinCompat) {
	archiveID := c.Param("id")
	ownerID := c.GetString("client_id")

	// 查归档记录
	a, err := s.repos.MemoryArchive.GetByID(archiveID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询归档记录失败: " + err.Error()})
		return
	}
	if a == nil {
		c.JSON(http.StatusNotFound, H{"error": "归档记录不存在"})
		return
	}
	// 归属校验
	if ownerID != "" && a.OriginalOwnerID != ownerID {
		log.Printf("[WARN][api] restoreMemory 越权访问 clientID=%s archiveID=%s", ownerID, archiveID)
		c.JSON(http.StatusForbidden, H{"error": "无权恢复该记忆"})
		return
	}
	// 已恢复
	if a.Restored {
		c.JSON(http.StatusBadRequest, H{"error": "该记忆已恢复"})
		return
	}
	// 已过期
	if time.Now().UTC().After(a.RestoreExpiresAt) {
		c.JSON(http.StatusBadRequest, H{"error": "恢复期限已过（归档后 30 天内可恢复）"})
		return
	}

	// 事务：写回主表 + 标记归档已恢复
	now := time.Now().UTC()
	m := &storage.Memory{
		ID:              a.ID,
		ProjectID:       a.OriginalProjectID,
		OwnerID:         a.OriginalOwnerID,
		Content:         a.Content,
		MemoryType:      a.MemoryType,
		Source:          a.Source,
		Importance:      50, // 恢复后重置重要度
		EmbeddingStatus: "pending",
		LastAccessedAt:  now, // 恢复后重置访问时间
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       now,
	}
	if err := s.repos.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(m).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.MemoryArchive{}).Where("id = ?", archiveID).
			Update("restored", true).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Printf("[ERROR][api] restoreMemory 恢复事务失败 id=%s err=%v", archiveID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "恢复失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] restoreMemory 恢复事务成功 id=%s", archiveID)

	// 恢复后触发重新 embed
	if s.memorySvc != nil {
		s.memorySvc.MaybeEmbedPending(c.Request().Context(), a.OriginalProjectID, ownerID)
		s.memorySvc.InvalidateProjectCache(c.Request().Context(), a.OriginalProjectID)
	}
	c.JSON(http.StatusOK, H{"status": "restored", "id": archiveID, "memory": m})
}

// listArchivedMemories GET /projects/:id/memories/archived
// 列出某项目的归档记忆（含已恢复）
func (s *Server) listArchivedMemories(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	archives, err := s.repos.MemoryArchive.ListByProject(projectID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询归档记忆失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"archives": archives, "total": len(archives)})
}

// triggerTTL POST /memory/ttl/run
// 手动触发一次 TTL 归档任务（运维用，不影响定时调度）
// 归档超14周未访问的记忆 + 物理删除超30天未恢复的归档记录
func (s *Server) triggerTTL(c *GinCompat) {
	if s.ttlScheduler == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "TTL 调度器未初始化"})
		return
	}
	archived, failed, cleaned, running := s.ttlScheduler.RunOnce(c.Request().Context())
	if running {
		log.Printf("[INFO][api] triggerTTL 任务正在执行中，拒绝并发触发")
		c.JSON(http.StatusConflict, H{
			"status":  "running",
			"message": "TTL 任务正在执行中（定时任务或手动触发），请稍后重试",
		})
		return
	}
	log.Printf("[INFO][api] triggerTTL 完成 archived=%d failed=%d cleaned=%d", archived, failed, cleaned)
	c.JSON(http.StatusOK, H{
		"status":         "done",
		"archived_count": archived,
		"failed_count":   failed,
		"cleaned_count":  cleaned,
		"ttl_weeks":      14,
		"restore_days":   30,
		"importance_exempt_threshold": 80,
	})
}

// ==================== 已压缩消息：查询 + 恢复 + TTL ====================

// listSummarizedMessages GET /sessions/:id/messages/summarized
// 查询某 Session 已被压缩的消息（供前端展示 + 手动恢复用）
// 分页参数：?limit=20&offset=0
func (s *Server) listSummarizedMessages(c *GinCompat) {
	sessionID := c.Param("id")
	ownerID := c.GetString("client_id")

	// owner 校验：确认 session 属于当前用户
	session, err := s.repos.Session.GetByID(sessionID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询 session 失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "session 不存在"})
		return
	}
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] listSummarizedMessages 越权访问 clientID=%s sessionID=%s", ownerID, sessionID)
		c.JSON(http.StatusForbidden, H{"error": "无权操作该 session"})
		return
	}

	limit := 20
	offset := 0
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	if o := c.Query("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	msgs, err := s.repos.Message.GetSummarizedBySessionID(sessionID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询已压缩消息失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{
		"messages": msgs,
		"total":    len(msgs),
		"limit":     limit,
		"offset":    offset,
	})
}

// restoreMessage POST /sessions/:id/messages/:mid/restore
// 恢复单条已压缩消息（重置 restored_at = now，相当于再给 7 天 TTL 窗口期）
// 注意：恢复不会把消息加回 loadHistory（summarized 仍为 true），仅用于前端展示 + TTL 延期
// 用户若想真正把消息加回上下文，需要人工合并摘要或重新发起对话
func (s *Server) restoreMessage(c *GinCompat) {
	sessionID := c.Param("id")
	msgIDStr := c.Param("mid")
	ownerID := c.GetString("client_id")

	// owner 校验
	session, err := s.repos.Session.GetByID(sessionID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询 session 失败: " + err.Error()})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, H{"error": "session 不存在"})
		return
	}
	if ownerID != "" && session.OwnerID != ownerID {
		log.Printf("[WARN][api] restoreMessage 越权访问 clientID=%s sessionID=%s", ownerID, sessionID)
		c.JSON(http.StatusForbidden, H{"error": "无权操作该 session"})
		return
	}

	msgID, err := strconv.ParseUint(msgIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "消息 ID 格式错误"})
		return
	}

	if err := s.repos.Message.RestoreMessage(uint(msgID)); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "恢复消息失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] 恢复已压缩消息 sessionID=%s msgID=%d", sessionID, msgID)
	c.JSON(http.StatusOK, H{
		"status":  "restored",
		"msg_id":  msgID,
		"message": "已重置 TTL，再给 7 天窗口期（注意：消息不会加回上下文，仅用于展示和延期）",
	})
}

// triggerMessageTTL POST /memory/message-ttl/run
// 手动触发一次消息 TTL 软删任务（运维用，不影响定时调度）
// 软删已压缩且超 7 天未恢复的消息
func (s *Server) triggerMessageTTL(c *GinCompat) {
	if s.msgTtlScheduler == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "消息 TTL 调度器未初始化"})
		return
	}
	deleted, running := s.msgTtlScheduler.RunOnce(c.Request().Context())
	if running {
		log.Printf("[INFO][api] triggerMessageTTL 任务正在执行中，拒绝并发触发")
		c.JSON(http.StatusConflict, H{
			"status":  "running",
			"message": "消息 TTL 任务正在执行中（定时任务或手动触发），请稍后重试",
		})
		return
	}
	log.Printf("[INFO][api] triggerMessageTTL 完成 deleted=%d", deleted)
	c.JSON(http.StatusOK, H{
		"status":        "done",
		"deleted_count": deleted,
		"ttl_days":      7,
		"rule":          "summarized=true 且 COALESCE(restored_at, summarized_at) + 7天 < now → 软删",
	})
}

// ==================== 子项目 session 列表 ====================

// listProjectSessions GET /projects/:id/sessions
func (s *Server) listProjectSessions(c *GinCompat) {
	projectID := c.Param("id")
	ownerID := c.GetString("client_id")
	if !s.checkProjectOwner(c, projectID, ownerID) {
		return
	}
	// 按 project_id 查 sessions（owner 校验）
	var sessions []storage.Session
	err := s.repos.DB.Where("project_id = ? AND owner_id = ?", projectID, ownerID).
		Order("pinned DESC, last_active_at DESC").
		Find(&sessions).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, H{"sessions": sessions, "total": len(sessions)})
}

// ==================== 工具函数 ====================

// checkProjectOwner 校验项目归属，失败时已写入响应；返回 false 表示校验失败
func (s *Server) checkProjectOwner(c *GinCompat, projectID, ownerID string) bool {
	p, err := s.repos.Project.GetByID(projectID, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "查询项目失败: " + err.Error()})
		return false
	}
	if p == nil {
		c.JSON(http.StatusNotFound, H{"error": "项目不存在"})
		return false
	}
	if ownerID != "" && p.OwnerID != ownerID {
		log.Printf("[WARN][api] checkProjectOwner 越权访问 clientID=%s projectID=%s", ownerID, projectID)
		c.JSON(http.StatusForbidden, H{"error": "无权操作该项目"})
		return false
	}
	return true
}
