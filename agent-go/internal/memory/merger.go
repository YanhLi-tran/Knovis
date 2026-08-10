package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"agent-go/internal/llm"
	"agent-go/internal/storage"
)

// ==================== 记忆合并(记忆生命周期 P2) ====================
// 触发: 项目记忆数 > MERGE_THRESHOLD(100)
// 流程: 召回同主题 fact → 聚类(cluster_size>=3) → LLM 合并 → 生成 summary + 原 fact 标记 merged_at
// 成本控制: 单次最多 MERGE_MAX_CLUSTERS(3) 簇, 每簇 1 次 LLM 调用

// Merger 记忆合并器
type Merger struct {
	svc      *Service
	repos    *storage.Repositories
	threshold int
	maxClusters int
	minClusterSize int
}

// NewMerger 创建合并器
func NewMerger(svc *Service, repos *storage.Repositories) *Merger {
	return &Merger{
		svc:      svc,
		repos:    repos,
		threshold: 100,     // MERGE_THRESHOLD
		maxClusters: 3,     // MERGE_MAX_CLUSTERS
		minClusterSize: 3,  // MERGE_MIN_CLUSTER_SIZE
	}
}

// MaybeMerge 检查触发条件并执行合并(供 extractor 5 轮周期调用)
func (m *Merger) MaybeMerge(ctx context.Context, projectID, ownerID string, provider llm.Provider) {
	if provider == nil {
		return
	}
	// 归档项目冻结(记忆生命周期 P3): 归档项目不合并
	if p, err := m.svc.repos.Project.GetByID(projectID, ownerID); err == nil && p != nil && p.IsArchived {
		return
	}

	// 召回 fact 类型记忆(未合并、未删除)
	facts, err := m.svc.repos.Memory.ListByProject(projectID, ownerID, 200)
	if err != nil {
		log.Printf("[merger] ListByProject 失败: %v", err)
		return
	}
	var factList []storage.Memory
	for _, f := range facts {
		if f.MemoryType == "fact" && f.MergedAt == nil && f.DeletedAt.Time.IsZero() {
			factList = append(factList, f)
		}
	}
	// 触发条件: fact 记忆数 >= minClusterSize(否则无簇可合)
	if len(factList) < m.minClusterSize {
		return
	}

	// 聚类(简单关键词重叠聚类)
	clusters := m.clusterByTopic(factList)
	if len(clusters) == 0 {
		return
	}

	// 对每个簇调 LLM 合并(最多 maxClusters)
	merged := 0
	for i, cluster := range clusters {
		if i >= m.maxClusters {
			break
		}
		if len(cluster) < m.minClusterSize {
			continue
		}
		summaries, err := m.llmMerge(ctx, cluster, provider)
		if err != nil {
			log.Printf("[merger] 合并失败 cluster=%d: %v", i, err)
			continue
		}
		for _, s := range summaries {
			if err := m.persistMerge(ctx, projectID, ownerID, cluster, s); err == nil {
				merged++
			}
		}
	}
	log.Printf("[merger] 合并完成 project=%s, 生成 %d 条 summary(来源 %d 簇)", projectID, merged, min(len(clusters), m.maxClusters))
}

// clusterByTopic 简单聚类: 按内容关键词重叠度(标题/公司名等)
// 用共享 token 数 >= 2 判定同主题
func (m *Merger) clusterByTopic(facts []storage.Memory) [][]storage.Memory {
	var clusters [][]storage.Memory
	used := make(map[string]bool)
	for i := 0; i < len(facts); i++ {
		if used[facts[i].ID] {
			continue
		}
		cluster := []storage.Memory{facts[i]}
		used[facts[i].ID] = true
		ti := tokenSet(facts[i].Content)
		for j := i + 1; j < len(facts); j++ {
			if used[facts[j].ID] {
				continue
			}
			tj := tokenSet(facts[j].Content)
			if sharedTokens(ti, tj) >= 2 {
				cluster = append(cluster, facts[j])
				used[facts[j].ID] = true
			}
		}
		if len(cluster) >= m.minClusterSize {
			clusters = append(clusters, cluster)
		}
	}
	return clusters
}

// tokenSet 提取内容关键词(按常见分隔符切分 + 过滤短词)
func tokenSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '，' || r == '。' || r == '、' || r == '：' || r == ';' || r == ',' || r == '(' || r == ')'
	}) {
		tok = strings.TrimSpace(tok)
		if len([]rune(tok)) >= 2 {
			out[tok] = true
		}
	}
	return out
}

func sharedTokens(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

// llmMerge 调 LLM 合并同主题 fact → 1-2 条 summary
func (m *Merger) llmMerge(ctx context.Context, cluster []storage.Memory, provider llm.Provider) ([]struct {
	Content    string `json:"content"`
	Importance int    `json:"importance"`
}, error) {
	const prompt = `你是记忆合并助手。以下是对同一主题的多条事实记忆，请合并为 1-2 条更精炼的 summary。
要求:
1. 保留所有关键数字、日期、结论
2. 去除重复信息
3. 合并后内容不超过 200 字
4. 输出 JSON 数组，每个元素含 content 和 importance(取原记忆中最高的)

只输出 JSON 数组，不要其他文字。`

	// 构造输入
	var sb strings.Builder
	sb.WriteString("[\n")
	for _, f := range cluster {
		sb.WriteString(fmt.Sprintf("  {\"id\": \"%s\", \"content\": \"%s\", \"importance\": %d},\n",
			f.ID, strings.ReplaceAll(f.Content, `"`, `\"`), f.Importance))
	}
	sb.WriteString("]")

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: sb.String()},
	}
	req := llm.ChatRequest{Messages: messages}

	var contentBuilder strings.Builder
	streamCh := provider.ChatStream(ctx, req)
	for chunk := range streamCh {
		if chunk.FinishReason == "error" {
			return nil, fmt.Errorf("LLM 流式错误")
		}
		if chunk.DeltaContent != "" {
			contentBuilder.WriteString(chunk.DeltaContent)
		}
	}
	raw := contentBuilder.String()
	if raw == "" {
		return nil, fmt.Errorf("LLM 返回空内容")
	}

	// 清理 markdown 代码块
	raw = cleanJSONBlock(raw)

	var items []struct {
		Content    string `json:"content"`
		Importance int    `json:"importance"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("解析失败: %v", err)
	}
	return items, nil
}

// persistMerge 入库: 新 summary + 原 fact 标记 merged_at + 向量更新
func (m *Merger) persistMerge(ctx context.Context, projectID, ownerID string, cluster []storage.Memory, summary struct {
	Content    string `json:"content"`
	Importance int    `json:"importance"`
}) error {
	if summary.Content == "" {
		return fmt.Errorf("summary 内容为空")
	}

	// 1. 构造 merged_from(原 fact ID 数组)
	ids := make([]string, 0, len(cluster))
	maxImp := 0
	for _, f := range cluster {
		ids = append(ids, f.ID)
		if f.Importance > maxImp {
			maxImp = f.Importance
		}
	}
	mergedFromJSON, _ := json.Marshal(ids)

	// 2. 新建 summary 记忆
	newSummary := &storage.Memory{
		ID:                   newMemoryUUID(),
		ProjectID:            projectID,
		OwnerID:              ownerID,
		Content:              summary.Content,
		MemoryType:           "summary",
		Source:               "merge",
		Importance:           maxImp,
		EffectiveImportance:  maxImp,
		EmbeddingStatus:      "pending",
		MergedFrom:           string(mergedFromJSON),
	}
	if err := m.svc.repos.Memory.Create(newSummary); err != nil {
		log.Printf("[merger] 创建 summary 失败: %v", err)
		return err
	}

	// 3. 原 fact 标记 merged_at(软删除语义, 不设 deleted_at)
	for _, f := range cluster {
		now := time.Now().UTC()
		if err := m.svc.repos.Memory.UpdateFields(f.ID, ownerID, map[string]any{
			"merged_at": now,
		}); err != nil {
			log.Printf("[merger] 标记 merged_at 失败 id=%s: %v", f.ID, err)
		}
	}

	// 4. 向量更新: 原 fact 从 Chroma 移除(异步, 不阻断)
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// 删除原 fact 向量
		idsToDelete := make([]string, 0, len(cluster))
		for _, f := range cluster {
			idsToDelete = append(idsToDelete, f.ID)
		}
		if _, err := m.svc.client.Delete(cctx, projectID, idsToDelete); err != nil {
			log.Printf("[merger] 删除原 fact 向量失败: %v", err)
		}
		// 新 summary 待 embed(标记 pending, 由 MaybeEmbedPending 处理)
	}()

	log.Printf("[merger] 合并生成 summary id=%s (from %d facts, importance=%d)", newSummary.ID[:12], len(cluster), maxImp)
	return nil
}

// min 返回较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
