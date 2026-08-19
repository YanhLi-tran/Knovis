package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"agent-go/internal/llm"
	"agent-go/internal/storage"
)

// 记忆自动提取相关常量
const (
	keywordImportance = 30   // 关键词初始重要度
	factImportance    = 50   // 事实记忆初始重要度
	dedupThreshold    = 0.92 // cosine 相似度判重阈值（>0.92 视为重复）
	importanceBump    = 5    // 命中/检索时重要度提升
	importanceMax     = 100  // 重要度上限
	llmExtractRounds  = 5    // 累计 N 轮触发一次 LLM 深度提取
)

// Extractor 记忆自动提取器（混合策略：关键词即时 + LLM 延迟 + Go 端去重拦截）
type Extractor struct {
	svc *Service
}

// NewExtractor 创建提取器
func NewExtractor(svc *Service) *Extractor {
	return &Extractor{svc: svc}
}

// ExtractFromTurn 从一轮对话提取关键词并入库（异步调用，不阻断对话）
// 流程：Python /extract_keywords → Go 去重拦截（/search cosine>0.92）→ 入库 agent_memories
func (e *Extractor) ExtractFromTurn(ctx context.Context, projectID, ownerID, sessionID string, round int, query, answer string) {
	if e.svc == nil || e.svc.client == nil || projectID == "" || ownerID == "" {
		log.Printf("[WARN][extractor] ExtractFromTurn 提前返回 (svc_nil=%v, projectID=%q, ownerID=%q)", e.svc == nil, projectID, ownerID)
		return
	}

	// 1) 关键词即时提取（jieba 分词 + TF-IDF）
	kws, err := e.svc.client.ExtractKeywords(ctx, []string{query, answer}, 10)
	if err != nil {
		log.Printf("[extractor] 关键词提取失败（跳过）: %v", err)
		return
	}

	// 2) 逐个去重 + 入库
	for _, kw := range kws {
		e.dedupAndCreate(ctx, projectID, ownerID, sessionID, round, kw.Word, "keyword", keywordImportance)
	}

	// 3) 检查是否达 embed 阈值，触发批量 embed
	e.maybeEmbed(ctx, projectID, ownerID)
}

// MaybeLLMExtract 累计轮次达阈值时触发 LLM 深度提取（输出结构化 JSON）
// 触发条件：session 轮次为 5 的倍数
func (e *Extractor) MaybeLLMExtract(ctx context.Context, projectID, ownerID, sessionID string, provider llm.Provider) {
	if e.svc == nil || projectID == "" || sessionID == "" || provider == nil {
		return
	}

	// 轮次计数：查 session 当前最大轮次
	rounds, err := e.svc.repos.Message.CountRounds(sessionID)
	if err != nil {
		log.Printf("[extractor] 查询轮次失败（跳过 LLM 提取）: %v", err)
		return
	}
	// 每 5 轮触发一次
	if rounds == 0 || rounds%llmExtractRounds != 0 {
		return
	}

	// 获取最近 5 轮对话
	msgs, err := e.svc.repos.Message.GetRecentBySessionID(sessionID, llmExtractRounds)
	if err != nil {
		log.Printf("[ERROR][extractor] GetRecentBySessionID 失败 (session=%s): %v", sessionID, err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	// 拼装对话文本（仅 user + assistant 最终回答，跳过 observe/tool 阶段）
	// user 消息落库含动态后缀（记忆/时间/上下文状态，KV 缓存优化），提取前剥离取用户原话
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role == "user" && m.Content != "" {
			sb.WriteString("用户: " + StripDynamicSuffix(m.Content) + "\n")
		} else if m.Role == "assistant" && m.Content != "" && m.Stage != "observe" {
			sb.WriteString("助手: " + m.Content + "\n")
		}
	}
	dialogText := strings.TrimSpace(sb.String())
	if dialogText == "" {
		log.Printf("[INFO][extractor] dialogText 为空，跳过 LLM 提取 (session=%s)", sessionID)
		return
	}

	e.doLLMExtract(ctx, projectID, ownerID, sessionID, rounds, dialogText, provider)

	// 记忆生命周期 P2: 记忆合并(同主题 fact 聚类 → LLM 生成 summary)
	// 每 5 轮触发一次, 复用 provider; 失败降级不影响主流程
	if e.svc.merger != nil {
		e.svc.merger.MaybeMerge(ctx, projectID, ownerID, provider)
	}
}

// doLLMExtract 调 LLM 提取结构化记忆（流式收集完整 JSON 后解析）
func (e *Extractor) doLLMExtract(ctx context.Context, projectID, ownerID, sessionID string, round int, dialogText string, provider llm.Provider) {
	const prompt = `你是一个记忆提取助手。请从以下对话中提取值得长期记忆的关键信息，包括：事实、用户偏好、事件、需求等。
输出 JSON 数组，每个元素包含：
- content: 记忆内容（简洁陈述）
- type: 类型（fact/preference/event/summary/requirement）
- importance: 重要度 1-100

只输出 JSON 数组，不要其他文字。若无值得记忆的内容，输出 []。`

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: dialogText},
	}
	req := llm.ChatRequest{Messages: messages}

	// 流式收集完整内容
	var contentBuilder strings.Builder
	streamCh := provider.ChatStream(ctx, req)
	for chunk := range streamCh {
		if chunk.FinishReason == "error" {
			log.Printf("[extractor] LLM 深度提取失败（流式错误）")
			return
		}
		if chunk.DeltaContent != "" {
			contentBuilder.WriteString(chunk.DeltaContent)
		}
	}
	raw := contentBuilder.String()
	if raw == "" {
		log.Printf("[WARN][extractor] LLM 返回空内容 (session=%s, round=%d)", sessionID, round)
		return
	}

	// 清理可能的 markdown 代码块包裹
	raw = cleanJSONBlock(raw)

	// 解析 JSON
	var items []struct {
		Content    string `json:"content"`
		Type       string `json:"type"`
		Importance int    `json:"importance"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		log.Printf("[extractor] LLM 提取结果解析失败（raw=%s）: %v", truncate(raw, 200), err)
		return
	}

	// 去重 + 入库
	for _, it := range items {
		if it.Content == "" {
			continue
		}
		imp := it.Importance
		if imp <= 0 {
			imp = factImportance
		}
		memType := it.Type
		if memType == "" {
			memType = "fact"
		}
		e.dedupAndCreate(ctx, projectID, ownerID, sessionID, round, it.Content, memType, imp)
	}

	// embed 阈值检查
	e.maybeEmbed(ctx, projectID, ownerID)
	log.Printf("[extractor] LLM 深度提取完成（项目 %s，提取 %d 条记忆）", projectID, len(items))
}

// dedupAndCreate 去重拦截：查同项目相似记忆（RAG raw cosine>0.92）
// 命中：更新 importance + last_accessed_at（不新增）
// 未命中：新建 agent_memories 记录（embedding_status=pending）
func (e *Extractor) dedupAndCreate(ctx context.Context, projectID, ownerID, sessionID string, round int, content, memType string, importance int) {
	// 去重：调 Python /search 查相似
	// 用 RAGRawScore(绝对 cosine)判重,不用融合分 score:
	// 融合分受 min-max 归一化 + keyword 降权影响,不是绝对相似度,误判会导致重复写入
	results, err := e.svc.client.Search(ctx, projectID, content, 3)
	if err != nil {
		log.Printf("[WARN][extractor] 去重检索失败，降级直接新建 (project=%s): %v", projectID, err)
	} else {
		for _, r := range results {
			raw := r.RAGRawScore
			if raw == 0 { // 兼容旧响应缺字段:回退融合分
				raw = r.Score
			}
			if raw >= dedupThreshold {
				// 命中：更新 importance（+5，上限100）+ last_accessed_at
			e.bumpExistingMemory(r.ID, ownerID)
			return
			}
		}
	}

	// 未命中：新建（embedding_status=pending，下次达 embed 阈值时批量生成向量）
	m := &storage.Memory{
		ID:              newMemoryUUID(),
		ProjectID:       projectID,
		OwnerID:         ownerID,
		Content:         content,
		MemoryType:      memType,
		Source:          "auto_extract",
		SourceSessionID: sessionID,
		SourceRound:     round,
		Importance:      importance,
		EmbeddingStatus: "pending",
	}
	if err := e.svc.repos.Memory.Create(m); err != nil {
		log.Printf("[extractor] 创建记忆失败（content=%s）: %v", truncate(content, 60), err)
	}
}

// bumpExistingMemory 更新已存在记忆的 importance + last_accessed_at + effective_importance
func (e *Extractor) bumpExistingMemory(id, ownerID string) {
	m, err := e.svc.repos.Memory.GetByID(id, ownerID)
	if err != nil {
		log.Printf("[WARN][extractor] GetByID 失败 (id=%s): %v", id, err)
		return
	}
	if m == nil {
		return
	}
	// 归档项目冻结(记忆生命周期 P3): 归档项目记忆检索只读, 不更新任何字段
	if p, err := e.svc.repos.Project.GetByID(m.ProjectID, ownerID); err == nil && p != nil && p.IsArchived {
		return
	}
	newImp := m.Importance + importanceBump
	if newImp > importanceMax {
		newImp = importanceMax
	}
	// 衰减后的有效重要度同步 +5(上限 100)(记忆生命周期 P0, 方案 §3.5)
	newEff := m.EffectiveImportance + importanceBump
	if newEff > importanceMax {
		newEff = importanceMax
	}
	fields := map[string]any{
		"importance":           newImp,
		"effective_importance": newEff,
		"last_accessed_at":     time.Now().UTC(),
	}
	// 唤起: hot 记忆命中时 last_accessed_at 更新后, 90 天窗口自动刷新(分层不会误降级)
	if m.Tier == "cold" {
		fields["tier"] = "hot" // 冷记忆被检索命中 → 回到热层(记忆生命周期 P1 唤起)
	}
	if err := e.svc.repos.Memory.UpdateFields(id, ownerID, fields); err != nil {
		log.Printf("[WARN][extractor] UpdateFields 失败 (id=%s): %v", id, err)
	}
}

// maybeEmbed 检查 embed 阈值，达阈值则异步批量 embed（复用 Service.MaybeEmbedPending）
func (e *Extractor) maybeEmbed(ctx context.Context, projectID, ownerID string) {
	e.svc.MaybeEmbedPending(ctx, projectID, ownerID)
}

// cleanJSONBlock 清理 markdown 代码块包裹（```json ... ```）
func cleanJSONBlock(raw string) string {
	s := strings.TrimSpace(raw)
	// 去除开头 ```json 或 ```
	if strings.HasPrefix(s, "```") {
		// 去掉第一行
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
	}
	// 去除结尾 ```
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}
	return strings.TrimSpace(s)
}

// truncate 截断字符串用于日志
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// newMemoryUUID 生成 UUID v4（避免引入额外依赖）
func newMemoryUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
