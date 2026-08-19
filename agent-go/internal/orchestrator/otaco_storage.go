package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-go/internal/llm"
	"agent-go/internal/memory"
	"agent-go/internal/storage"
)

// persister 负责 OTACO 过程的持久化（加载历史 / 保存消息 / 异步摘要 / 标题生成）
type persister struct {
	repos *storage.Repositories

	// 并发控制
	globalSem chan struct{} // 全局并发上限（容量 20）
	userConns sync.Map      // clientID -> *int64，单用户并发上限 3

	// 摘要生成去重
	summarizing sync.Map // sessionID -> bool，防止重复生成摘要

	// 标题生成去重
	titling sync.Map // sessionID -> bool，防止重复生成标题
}

const (
	// 按需压缩模式：无固定滑动窗口；历史全量拼接（token 预算内），
	// 上下文占比 ≥80% 由 LLM 自主调 summarize_history 压缩，≥90% Go 强制压缩
	globalMaxConcurrent  = 20  // 全局 OTACO 并发上限
	perUserMaxConcurrent = 3   // 单用户 OTACO 并发上限
)

// newPersister 创建 persister
func newPersister(repos *storage.Repositories) *persister {
	return &persister{
		repos:     repos,
		globalSem: make(chan struct{}, globalMaxConcurrent),
	}
}

// acquireConcurrency 占用并发槽，返回释放函数；失败返回 false
func (p *persister) acquireConcurrency(clientID string) (release func(), ok bool) {
	// 全局信号量
	select {
	case p.globalSem <- struct{}{}:
	default:
		return nil, false
	}

	// 单用户计数
	counter := p.getOrCreateUserCounter(clientID)
	for {
		cur := atomic.LoadInt64(counter)
		if cur >= int64(perUserMaxConcurrent) {
			// 释放全局槽
			<-p.globalSem
			return nil, false
		}
		if atomic.CompareAndSwapInt64(counter, cur, cur+1) {
			break
		}
	}

	return func() {
		atomic.AddInt64(counter, -1)
		<-p.globalSem
	}, true
}

// loadHistory 加载历史对话，转换为 llm.Message
// 按需压缩模式（无固定窗口）：
//   - 只加载未压缩的历史轮次（已压缩的由摘要代表，不再进上下文）
//   - 布局：[system 稳定前缀] + [历史轮次（从旧到新，token 预算内截断）] + [摘要（独立消息放最后，KV Cache 友好）]
// 返回：messages, 当前 session 的 maxRound（用于新对话接续轮次）, error
func (p *persister) loadHistory(ctx context.Context, sessionID string, systemPrompt string) ([]llm.Message, int, error) {
	if sessionID == "" {
		return nil, 0, nil
	}

	session, err := p.repos.Session.GetByID(sessionID, "")
	if err != nil {
		log.Printf("[ERROR][otaco] loadHistory GetByID 失败 sessionID=%s: %v", sessionID, err)
		return nil, 0, err
	}
	if session == nil {
		log.Printf("[WARN][otaco] loadHistory session 不存在 sessionID=%s", sessionID)
		return nil, 0, fmt.Errorf("session %s 不存在", sessionID)
	}

	// 查询当前 maxRound（用于新对话接续轮次）
	maxRound, err := p.repos.Message.CountRounds(sessionID)
	if err != nil {
		log.Printf("[ERROR][otaco] loadHistory CountRounds 失败 sessionID=%s: %v", sessionID, err)
		return nil, 0, err
	}

	messages := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}

	// 拉取全部未压缩历史（已压缩的由摘要代表）
	unsummarized, err := p.repos.Message.GetUnsummarizedBySessionID(sessionID)
	if err != nil {
		log.Printf("[ERROR][otaco] loadHistory GetUnsummarizedBySessionID 失败 sessionID=%s: %v", sessionID, err)
		return nil, 0, err
	}

	// 组装历史轮次（从旧到新；跳过 observe 决策记录与空 assistant 消息）
	for _, m := range unsummarized {
		switch m.Role {
		case "user":
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: m.Content})
		case "assistant":
			if m.Stage == "observe" {
				continue
			}
			msg := llm.Message{Role: llm.RoleAssistant, Content: m.Content}
			if m.ToolCalls != "" {
				var tcs []llm.ToolCall
				if json.Unmarshal([]byte(m.ToolCalls), &tcs) == nil {
					msg.ToolCalls = tcs
				}
			}
			// 防御：跳过 content 和 tool_calls 都为空的 assistant 消息（DeepSeek 会返回 400）
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			messages = append(messages, msg)
		case "tool":
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		}
	}

	// 摘要作为独立消息放消息列表最末尾（易变内容后置，KV Cache 友好）
	// 已压缩的历史轮次不再进上下文，由该摘要代表
	if session.Summary != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "## 历史对话摘要\n" + session.Summary,
		})
	}

	// token 预算截断：保留最新历史轮次，从最旧截断超预算部分（保证最新细节 + 摘要一定在）
	maxCtxLen := p.effectiveMaxContextLen(sessionID)
	if maxCtxLen > 0 {
		estimator := llm.NewTokenEstimator()
		total := estimator.EstimateMessages(messages)
		for total > maxCtxLen && len(messages) > 1 {
			// 从头部（最旧）丢，保留 system 与末尾摘要
			messages = append(messages[:1], messages[2:]...)
			total = estimator.EstimateMessages(messages)
		}
	}

	return messages, maxRound, nil
}

// effectiveMaxContextLen 计算生效的上下文长度（token），优先级：用户自定义 > 项目级 > 默认 64000
// 用户可在前端"Agent 行为设置"里配置（UserConfig.MaxContextLength，上限 1M 适配 DeepSeek 长上下文）
func (p *persister) effectiveMaxContextLen(sessionID string) int {
	session, err := p.repos.Session.GetByID(sessionID, "")
	if err != nil || session == nil {
		return storage.DefaultMaxContextLength
	}
	fallback := storage.DefaultMaxContextLength
	if session.ProjectID != "" {
		if proj, perr := p.repos.Project.GetByID(session.ProjectID, ""); perr == nil && proj != nil && proj.MaxContextLength > 0 {
			fallback = proj.MaxContextLength
		}
	}
	// 用户级优先（0=未设置 → 用项目级/默认）
	if session.OwnerID != "" {
		if uc, uerr := p.repos.UserConfig.GetByUserID(session.OwnerID); uerr == nil && uc != nil {
			return uc.Behavior().EffectiveMaxContextLength(fallback)
		}
	}
	return fallback
}

// saveRound 保存一轮 OTACO 的全部消息（原子事务）
// round: 当前轮次（从 1 开始）
// query: 本轮用户输入（仅第 0 轮需要保存；含动态后缀的完整 user 消息，
//
//	与发给 LLM 的内容一致，保证 loadHistory 重建 token 流 = 本次请求 token 流，
//	跨请求缓存前缀稳定；消费侧用 stripDynamicSuffix 剥离后缀）
//
// observe: Observation 决策信息
// thought: assistant 思考内容
// toolCalls: assistant 发起的工具调用
// toolResults: 工具返回结果
// finalAnswer: 非空表示本轮有最终答案
// apiKey/provider: 用于异步摘要生成的 LLM 调用
func (p *persister) saveRound(sessionID string, round int, query string, observe *observeInfo, thought string, toolCalls []llm.ToolCall, toolResults []toolResultRecord, finalAnswer string) error {
	if sessionID == "" {
		return nil
	}
	var msgs []storage.Message
	now := time.Now().UTC()

	// 用户输入（仅本轮有 query 时）
	if query != "" {
		msgs = append(msgs, storage.Message{
			SessionID: sessionID,
			Round:     round,
			Role:      "user",
			Stage:     "",
			Content:   query,
			CreatedAt: now,
		})
	}

	// Observation 决策
	if observe != nil {
		msgs = append(msgs, storage.Message{
			SessionID: sessionID,
			Round:     round,
			Role:      "assistant",
			Stage:     "observe",
			Decision:  observe.decision,
			Reason:    observe.reason,
			CreatedAt: now,
		})
	}

	// Thought + ToolCalls（assistant 消息）
	if thought != "" || len(toolCalls) > 0 {
		m := storage.Message{
			SessionID: sessionID,
			Round:     round,
			Role:      "assistant",
			Stage:     "think",
			Content:   thought,
			CreatedAt: now,
		}
		if len(toolCalls) > 0 {
			b, _ := json.Marshal(toolCalls)
			m.ToolCalls = string(b)
		}
		msgs = append(msgs, m)
	}

	// 工具结果（check 阶段）
	for _, r := range toolResults {
		content := r.content
		if r.errMsg != "" {
			content = fmt.Sprintf(`{"error":"%s"}`, r.errMsg)
		}
		msgs = append(msgs, storage.Message{
			SessionID:  sessionID,
			Round:      round,
			Role:       "tool",
			Stage:      "check",
			ToolCallID: r.toolCallID,
			Content:    content,
			CreatedAt:  now,
		})
	}

	// 最终答案
	if finalAnswer != "" {
		msgs = append(msgs, storage.Message{
			SessionID: sessionID,
			Round:     round,
			Role:      "assistant",
			Stage:     "output",
			Content:   finalAnswer,
			CreatedAt: now,
		})
	}

	if len(msgs) == 0 {
		return nil
	}

	if err := p.repos.Message.CreateBatch(msgs); err != nil {
		log.Printf("[ERROR][otaco] saveRound CreateBatch 失败 sessionID=%s round=%d: %v", sessionID, round, err)
		return fmt.Errorf("保存 OTACO 轮次失败: %w", err)
	}

	// 更新 Session.LastActiveAt
	if err := p.repos.Session.TouchLastActive(sessionID, ""); err != nil {
		log.Printf("[WARN][otaco] TouchLastActive 失败 sessionID=%s: %v", sessionID, err)
	}

	return nil
}

// observeInfo Observation 决策信息
type observeInfo struct {
	decision string // pass / retry / rollback
	reason   string
}

// toolResultRecord 工具结果记录（用于持久化）
type toolResultRecord struct {
	toolCallID string
	toolName   string
	content    string
	errMsg     string
}

// summarizeHistory 同步压缩历史（按需压缩，无固定窗口）
// 策略（合并重压）：旧摘要 + 全部未压缩消息 一起交给 LLM，生成新的合并摘要
// - 查询全部 summarized=false 的消息（已压缩的由摘要代表，不再处理）
// - 拼接旧摘要 + 新消息，调 LLM 生成新摘要（长度可控）
// - 压缩成功后标记消息 summarized=true（后续 loadHistory 跳过，由摘要代表）
// - 同步执行：由 summarize_history 工具（LLM 自主）与 90% Go 强制调用
// 返回：本次压缩的消息条数；并发去重（同 session 已在压缩时返回 0, nil）
func (p *persister) summarizeHistory(sessionID string, apiKey string, provider llm.Provider) (int, error) {
	if _, loaded := p.summarizing.LoadOrStore(sessionID, true); loaded {
		return 0, nil // 已在压缩中（并发工具调用 + 90% 强制同时触发）
	}
	defer p.summarizing.Delete(sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 查询全部未压缩消息
	msgs, err := p.repos.Message.GetUnsummarizedBySessionID(sessionID)
	if err != nil {
		log.Printf("[ERROR][otaco] summarizeHistory GetUnsummarizedBySessionID 失败 sessionID=%s: %v", sessionID, err)
		return 0, err
	}
	if len(msgs) == 0 {
		log.Printf("[INFO][otaco] summarizeHistory 无未压缩消息 sessionID=%s", sessionID)
		return 0, nil
	}

	// 加载旧摘要（合并重压：旧摘要 + 全部未压缩消息一起压缩）
	session, err := p.repos.Session.GetByID(sessionID, "")
	if err != nil || session == nil {
		log.Printf("[WARN][otaco] summarizeHistory GetSession 失败 sessionID=%s: %v", sessionID, err)
		return 0, err
	}
	oldSummary := session.Summary

	// 拼接文本：旧摘要 + 新消息
	var sb strings.Builder
	if oldSummary != "" {
		sb.WriteString("## 已有摘要\n")
		sb.WriteString(oldSummary)
		sb.WriteString("\n\n## 新增对话内容\n")
	}
	for _, m := range msgs {
		content := m.Content
		// user 消息含动态后缀（记忆/时间/上下文状态），摘要输入剥离取用户原话
		if m.Role == "user" {
			content = memory.StripDynamicSuffix(content)
		}
		sb.WriteString(fmt.Sprintf("[第%d轮/%s/%s] %s\n", m.Round, m.Role, m.Stage, truncate(content, 500)))
	}

	// 调用 LLM 生成合并摘要
	summary, err := p.callSummaryLLM(ctx, oldSummary, sb.String(), apiKey, provider)
	if err != nil {
		log.Printf("[WARN][otaco] summarizeHistory 生成摘要失败 sessionID=%s: %v", sessionID, err)
		return 0, err
	}

	// 写入新摘要
	if err := p.repos.Session.UpdateSummary(sessionID, "", summary); err != nil {
		log.Printf("[WARN][otaco] summarizeHistory 写入摘要失败 sessionID=%s: %v", sessionID, err)
		return 0, err
	}

	// 标记消息为已压缩（后续 loadHistory 跳过，由摘要代表）
	ids := make([]uint, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if err := p.repos.Message.MarkSummarized(ids); err != nil {
		log.Printf("[WARN][otaco] summarizeHistory 标记已压缩失败 sessionID=%s: %v", sessionID, err)
		return 0, err
	}

	log.Printf("[INFO][otaco] 摘要生成完成 sessionID=%s 压缩消息数=%d 摘要长度=%d",
		sessionID, len(msgs), len([]rune(summary)))
	return len(msgs), nil
}

// callSummaryLLM 调用 LLM 生成摘要（合并重压）
// oldSummary: 已有的旧摘要（可能为空）
// content: 旧摘要 + 新消息拼接的完整文本
// 用 provider.ChatStream 发送摘要 prompt，收集流式响应
func (p *persister) callSummaryLLM(ctx context.Context, oldSummary string, content string, apiKey string, provider llm.Provider) (string, error) {
	systemPrompt := "你是一个对话摘要助手，负责将历史对话压缩成简洁摘要，保留关键信息（事实、决策、用户偏好、重要事件）。"
	userPrompt := "请将以下内容压缩成简洁的摘要。"
	if oldSummary != "" {
		userPrompt += "已有摘要会一并提供，请将新增对话内容合并到已有摘要中，生成新的完整摘要（不要简单拼接，要重新组织）。"
	} else {
		userPrompt += "这是首次压缩，请直接生成摘要。"
	}
	userPrompt += "摘要应控制在 500 字以内，保留关键信息，丢弃无关细节。\n\n" + content

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: userPrompt},
		},
	}

	ch := provider.ChatStream(ctx, req)
	var summary strings.Builder
	for chunk := range ch {
		if chunk.FinishReason == "error" {
			return "", fmt.Errorf("LLM 摘要调用失败")
		}
		if chunk.DeltaContent != "" {
			summary.WriteString(chunk.DeltaContent)
		}
	}

	result := strings.TrimSpace(summary.String())
	if result == "" {
		return "", fmt.Errorf("LLM 返回空摘要")
	}
	// 限制摘要长度（兜底，防止超长）
	return truncate(result, 2000), nil
}

// maybeGenerateTitle 首轮对话后异步生成 Session 标题
func (p *persister) maybeGenerateTitle(ctx context.Context, sessionID, query, apiKey string, provider llm.Provider) {
	// 检查是否已有标题（非"新对话"）
	session, err := p.repos.Session.GetByID(sessionID, "")
	if err != nil {
		log.Printf("[WARN][otaco] maybeGenerateTitle GetByID 失败 sessionID=%s: %v", sessionID, err)
		return
	}
	if session == nil {
		log.Printf("[WARN][otaco] maybeGenerateTitle session 不存在 sessionID=%s", sessionID)
		return
	}
	if session.Title != "新对话" {
		log.Printf("[INFO][otaco] maybeGenerateTitle 已有标题跳过 sessionID=%s title=%s", sessionID, session.Title)
		return
	}

	// 检查是否已有消息（首轮才生成）
	maxRound, err := p.repos.Message.CountRounds(sessionID)
	if err != nil {
		log.Printf("[WARN][otaco] maybeGenerateTitle CountRounds 失败 sessionID=%s: %v", sessionID, err)
		return
	}
	if maxRound > 1 {
		log.Printf("[INFO][otaco] maybeGenerateTitle 非首轮跳过 sessionID=%s maxRound=%d", sessionID, maxRound)
		return
	}

	// 去重
	if _, loaded := p.titling.LoadOrStore(sessionID, true); loaded {
		return
	}

	go func() {
		log.Printf("[INFO][otaco] 标题生成 goroutine 启动 sessionID=%s", sessionID)
		defer p.titling.Delete(sessionID)

		titleCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		title := p.generateTitle(titleCtx, query, apiKey, provider)
		if title == "" {
			log.Printf("[WARN][otaco] maybeGenerateTitle 生成标题为空 sessionID=%s", sessionID)
			return
		}
		if err := p.repos.Session.UpdateTitle(sessionID, "", title); err != nil {
			log.Printf("[WARN][otaco] maybeGenerateTitle 更新标题失败 sessionID=%s: %v", sessionID, err)
		}
	}()
}

// generateTitle 调用 LLM 生成标题
func (p *persister) generateTitle(ctx context.Context, query string, apiKey string, provider llm.Provider) string {
	// P0 简化：直接用 query 前 20 字
	// 后续改进：调用 LLM 生成 4-8 字标题
	title := strings.TrimSpace(query)
	if len([]rune(title)) > 20 {
		title = string([]rune(title)[:20])
	}
	if title == "" {
		title = "新对话"
	}
	return title
}

// getOrCreateUserCounter 获取或创建用户并发计数器
func (p *persister) getOrCreateUserCounter(clientID string) *int64 {
	actual, _ := p.userConns.LoadOrStore(clientID, new(int64))
	return actual.(*int64)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
