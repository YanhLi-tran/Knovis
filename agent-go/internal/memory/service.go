package memory

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"agent-go/internal/config"
	"agent-go/internal/storage"
)

// Service 记忆服务：协调全局记忆 + 项目记忆的加载、检索、注入
// 依赖：config.Manager（soul/rule 已在 orchestrator 注入，这里负责 user/project/memories）
type Service struct {
	cfg    *config.Manager
	repos  *storage.Repositories
	client *MemoryClient
	merger *Merger // 记忆生命周期 P2: 记忆合并器
}

// NewService 创建记忆服务
func NewService(cfg *config.Manager, repos *storage.Repositories, client *MemoryClient) *Service {
	s := &Service{cfg: cfg, repos: repos, client: client}
	s.merger = NewMerger(s, repos)
	return s
}

// LoadUserConfig 加载用户档案（Redis 缓存 → MySQL → 缓存回填）
// userID 为空或档案不存在时返回 nil（不阻断）
func (s *Service) LoadUserConfig(ctx context.Context, userID string) (*storage.UserConfig, error) {
	if userID == "" {
		return nil, nil
	}
	// 1) 缓存
	if s.repos.Cache != nil {
		var uc storage.UserConfig
		if ok, _ := s.repos.Cache.GetJSON(ctx, storage.UserConfigCacheKey(userID), &uc); ok {
			return &uc, nil
		}
	}
	// 2) MySQL
	uc, err := s.repos.UserConfig.GetByUserID(userID)
	if err != nil {
		log.Printf("[ERROR][memory] GetByUserID 失败 (user=%s): %v", userID, err)
		return nil, fmt.Errorf("加载用户档案失败: %w", err)
	}
	if uc == nil {
		return nil, nil
	}
	// 3) 回填缓存
	if s.repos.Cache != nil {
		if err := s.repos.Cache.SetJSON(ctx, storage.UserConfigCacheKey(userID), uc, storage.TTLUserConfig()); err != nil {
			log.Printf("[memory] 回填用户档案缓存失败: %v", err)
		}
	}
	return uc, nil
}

// LoadProject 加载项目信息（Redis 缓存 → MySQL → 缓存回填）
func (s *Service) LoadProject(ctx context.Context, projectID string) (*storage.Project, error) {
	if projectID == "" {
		return nil, nil
	}
	if s.repos.Cache != nil {
		var p storage.Project
		if ok, _ := s.repos.Cache.GetJSON(ctx, storage.ProjectInfoCacheKey(projectID), &p); ok {
			return &p, nil
		}
	}
	p, err := s.repos.Project.GetByID(projectID, "")
	if err != nil {
		log.Printf("[ERROR][memory] GetByID 失败 (project=%s): %v", projectID, err)
		return nil, fmt.Errorf("加载项目失败: %w", err)
	}
	if p == nil {
		return nil, nil
	}
	if s.repos.Cache != nil {
		if err := s.repos.Cache.SetJSON(ctx, storage.ProjectInfoCacheKey(projectID), p, storage.TTLProjectInfo()); err != nil {
			log.Printf("[memory] 回填项目信息缓存失败: %v", err)
		}
	}
	return p, nil
}

// InvalidateProjectCache 失效项目相关缓存（项目更新/删除记忆时调用）
func (s *Service) InvalidateProjectCache(ctx context.Context, projectID string) {
	if s.repos.Cache == nil || projectID == "" {
		return
	}
	log.Printf("[INFO][memory] 失效项目缓存 (project=%s)", projectID)
	_ = s.repos.Cache.Del(ctx, storage.ProjectMemoriesCacheKey(projectID), storage.ProjectInfoCacheKey(projectID))
}

// RetrieveProjectMemories 检索项目记忆 top-N（HTTP 调 Python /search）
// 直接用 Python 返回结果（含 content/memory_type/source/importance），省 N 次 MySQL 查询
// 降级策略：Python 服务不可用时，回退 MySQL ListByProject（按重要度+最近访问，无相关性排序）
func (s *Service) RetrieveProjectMemories(ctx context.Context, projectID, ownerID, query string, topK int) ([]storage.Memory, error) {
	if projectID == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 5
	}

	// 调用 Python 混合检索
	results, err := s.client.Search(ctx, projectID, query, topK)
	if err != nil {
		log.Printf("[memory] 混合检索失败，降级 MySQL 回退: %v", err)
		return s.fallbackListMemories(ctx, projectID, ownerID, topK)
	}
	if len(results) == 0 {
		return nil, nil
	}

	// 直接用 Python 返回结果构造 Memory（省 N 次 MySQL GetByID 查询）
	memories := make([]storage.Memory, 0, len(results))
	ids := make([]string, 0, len(results))
	for _, r := range results {
		memories = append(memories, storage.Memory{
			ID:         r.ID,
			Content:    r.Content,
			MemoryType: r.MemoryType,
			Source:     r.Source,
			Importance: r.Importance,
		})
		ids = append(ids, r.ID)
	}

	// 异步更新 last_accessed_at（LRU，不阻塞注入）
	go func(ids []string) {
		log.Printf("[INFO][memory] BatchTouchLastAccessed goroutine 启动 (ids=%d)", len(ids))
		if err := s.repos.Memory.BatchTouchLastAccessed(ids, ownerID); err != nil {
			log.Printf("[memory] 更新记忆访问时间失败: %v", err)
		}
	}(ids)

	return memories, nil
}

// fallbackListMemories 降级：MySQL 直接取（按重要度+最近访问）
func (s *Service) fallbackListMemories(ctx context.Context, projectID, ownerID string, topK int) ([]storage.Memory, error) {
	return s.repos.Memory.ListByProject(projectID, ownerID, topK)
}

// EmbedPendingMemories 批量生成 pending 记忆的 embedding（累计阈值触发）
// 返回成功 embed 的条数
func (s *Service) EmbedPendingMemories(ctx context.Context, projectID, ownerID string) (int, error) {
	if projectID == "" {
		return 0, nil
	}
	// 单批最多 32 条
	pending, err := s.repos.Memory.ListPendingEmbedding(projectID, ownerID, 32)
	if err != nil {
		log.Printf("[ERROR][memory] ListPendingEmbedding 失败 (project=%s): %v", projectID, err)
		return 0, fmt.Errorf("查询 pending 记忆失败: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	items := make([]EmbedItem, 0, len(pending))
	for _, m := range pending {
		items = append(items, EmbedItem{
			ID:         m.ID,
			Content:    m.Content,
			MemoryType: m.MemoryType,
			Source:     m.Source,
			Importance: m.Importance,
		})
	}

	n, err := s.client.Embed(ctx, projectID, items)
	if err != nil {
		// 标记失败（下次重试）
		for _, m := range pending {
			if ferr := s.repos.Memory.MarkEmbeddingFailed(m.ID, ownerID); ferr != nil {
				log.Printf("[WARN][memory] MarkEmbeddingFailed 失败 (id=%s): %v", m.ID, ferr)
			}
		}
		log.Printf("[ERROR][memory] Embed 失败 (project=%s, items=%d): %v", projectID, len(items), err)
		return 0, fmt.Errorf("embedding 失败: %w", err)
	}

	// 批量标记完成
	ids := make([]string, 0, len(pending))
	for _, m := range pending {
		ids = append(ids, m.ID)
	}
	if err := s.repos.Memory.BatchMarkEmbeddingDone(ids, ownerID); err != nil {
		log.Printf("[memory] 标记 embedding 完成失败: %v", err)
	}
	return n, nil
}

// ShouldEmbed 检查是否达到批量 embed 阈值（累计 N 轮）
func (s *Service) ShouldEmbed(ctx context.Context, projectID, ownerID string) bool {
	threshold := s.cfg.GetAppConfig().MemoryEmbedBatchRounds
	if threshold <= 0 {
		threshold = 5
	}
	count, err := s.repos.Memory.CountPendingEmbedding(projectID, ownerID)
	if err != nil {
		log.Printf("[WARN][memory] CountPendingEmbedding 失败 (project=%s): %v", projectID, err)
		return false
	}
	return count >= int64(threshold)
}

// MaybeEmbedPending 检查 embed 阈值，达阈值则异步批量 embed
// 供 extractor 和 API handler 复用
func (s *Service) MaybeEmbedPending(ctx context.Context, projectID, ownerID string) {
	if !s.ShouldEmbed(ctx, projectID, ownerID) {
		return
	}
	go func() {
		log.Printf("[INFO][memory] MaybeEmbedPending goroutine 启动 (project=%s)", projectID)
		n, err := s.EmbedPendingMemories(context.Background(), projectID, ownerID)
		if err != nil {
			log.Printf("[memory] 自动 embed 失败: %v", err)
		} else if n > 0 {
			log.Printf("[memory] 自动 embed %d 条记忆（项目 %s）", n, projectID)
		}
	}()
}

// BuildMemoryBlock 构建记忆注入块（插入到 system prompt 工具列表之后、时间之前）
// 分层顺序：user 档案(必注) → 项目信息(若有) → 项目记忆 top5(若有)
// 全部为空时返回空字符串
func (s *Service) BuildMemoryBlock(ctx context.Context, userID, projectID, query string) (string, error) {
	var sb strings.Builder
	hasContent := false

	// 1) 用户档案（必注）
	uc, err := s.LoadUserConfig(ctx, userID)
	if err != nil {
		log.Printf("[memory] 加载用户档案失败（跳过注入）: %v", err)
	} else if uc != nil {
		text := uc.RawText
		if text == "" {
			text = s.composeUserConfigText(uc)
		}
		if text != "" {
			sb.WriteString("## 用户档案\n")
			sb.WriteString(text)
			sb.WriteString("\n\n")
			hasContent = true
		}
	}

	// 2) 项目信息 + 项目记忆
	if projectID != "" {
		p, err := s.LoadProject(ctx, projectID)
		if err != nil {
			log.Printf("[memory] 加载项目失败（跳过项目记忆注入）: %v", err)
		} else if p != nil {
			// 项目元信息（归档项目仍注入上下文，便于 session 继续对话）
			projText := s.composeProjectText(p)
			if projText != "" {
				sb.WriteString("## 项目上下文\n")
				if p.IsArchived {
					sb.WriteString(projText + "\n（注意：该项目已归档，记忆检索已停用）\n\n")
				} else {
					sb.WriteString(projText)
					sb.WriteString("\n\n")
				}
				hasContent = true
			}

			// 项目记忆 RAG top5：归档项目跳过检索（停用记忆注入，session 仍可对话）
			if p.IsArchived {
				log.Printf("[memory] 项目 %s 已归档，跳过 RAG 注入", projectID)
			} else if s.cfg.GetAppConfig().MemoryInjectProject {
				memories, err := s.RetrieveProjectMemories(ctx, projectID, userID, query, 5)
				if err != nil {
					log.Printf("[memory] 检索项目记忆失败（跳过）: %v", err)
				} else if len(memories) > 0 {
					sb.WriteString("## 相关记忆（项目记忆 RAG top-5）\n")
					for i, m := range memories {
						sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, m.MemoryType, m.Content))
					}
					sb.WriteString("\n")
					hasContent = true
				}
			}
		}
	}

	// 3) @跨项目读取：解析 query 中的 @项目名，仅限自己的项目（owner_id 校验）
	if userID != "" && s.cfg.GetAppConfig().MemoryInjectProject {
		refs := ParseCrossProjectRefs(query)
		for _, refName := range refs {
			// 按项目名查用户自己的项目（非自己项目返回 nil，跳过）
			p, err := s.repos.Project.FindByName(userID, refName)
			if err != nil {
				log.Printf("[memory] 跨项目查询失败（@%s）: %v", refName, err)
				continue
			}
			if p == nil {
				// 项目不存在或不归属当前用户，静默跳过（不报错，不注入）
				continue
			}
			if p.ID == projectID {
				// 跳过当前项目（避免与上方项目记忆重复）
				continue
			}
			// 检索跨项目记忆 top3
			memories, err := s.RetrieveProjectMemories(ctx, p.ID, userID, query, 3)
			if err != nil {
				log.Printf("[WARN][memory] 跨项目记忆检索失败 (@%s, project=%s): %v", refName, p.ID, err)
				continue
			}
			if len(memories) == 0 {
				continue
			}
			sb.WriteString(fmt.Sprintf("## 跨项目参考（@%s）\n", refName))
			for i, m := range memories {
				sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, m.MemoryType, m.Content))
			}
			sb.WriteString("\n")
			hasContent = true
		}
	}

	if !hasContent {
		return "", nil
	}
	return sb.String(), nil
}

// ParseCrossProjectRefs 解析 query 中的 @项目名 引用
// 匹配 @ 后面的非空白字符（支持中文/英文/数字/下划线）
// 返回去重的项目名列表
func ParseCrossProjectRefs(query string) []string {
	var refs []string
	seen := map[string]bool{}
	// @项目名：@后跟非空白、非@的字符序列
	re := regexp.MustCompile(`@([^\s@]+)`)
	matches := re.FindAllStringSubmatch(query, -1)
	for _, m := range matches {
		if len(m) >= 2 {
			name := strings.TrimSpace(m[1])
			if name != "" && !seen[name] {
				seen[name] = true
				refs = append(refs, name)
			}
		}
	}
	return refs
}

// composeUserConfigText 拼装用户档案为可读文本（若 raw_text 为空时用）
func (s *Service) composeUserConfigText(uc *storage.UserConfig) string {
	var sb strings.Builder
	if uc.Location != "" {
		sb.WriteString(fmt.Sprintf("- 位置: %s\n", uc.Location))
	}
	if uc.BasicInfo != "" {
		sb.WriteString(fmt.Sprintf("- 基础信息: %s\n", uc.BasicInfo))
	}
	if uc.Preferences != "" {
		sb.WriteString(fmt.Sprintf("- 偏好: %s\n", uc.Preferences))
	}
	return strings.TrimSpace(sb.String())
}

// composeProjectText 拼装项目信息为可读文本
func (s *Service) composeProjectText(p *storage.Project) string {
	var sb strings.Builder
	if p.Name != "" {
		sb.WriteString(fmt.Sprintf("- 项目名称: %s\n", p.Name))
	}
	if p.Description != "" {
		sb.WriteString(fmt.Sprintf("- 项目描述: %s\n", p.Description))
	}
	if p.Context != "" {
		sb.WriteString(fmt.Sprintf("- 项目上下文: %s\n", p.Context))
	}
	if p.Rules != "" {
		sb.WriteString(fmt.Sprintf("- 项目规则: %s\n", p.Rules))
	}
	if p.KeyPoints != "" {
		sb.WriteString(fmt.Sprintf("- 记忆要点: %s\n", p.KeyPoints))
	}
	if p.UserDefined != "" {
		sb.WriteString(fmt.Sprintf("- 用户备注: %s\n", p.UserDefined))
	}
	return strings.TrimSpace(sb.String())
}

// GetClient 暴露 MemoryClient（API 层 embed/delete 用）
func (s *Service) GetClient() *MemoryClient { return s.client }
