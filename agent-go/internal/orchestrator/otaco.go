package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"agent-go/internal/config"
	"agent-go/internal/llm"
	"agent-go/internal/memory"
	"agent-go/internal/storage"
	"agent-go/internal/tools"
	"agent-go/internal/tools/skill"
)

// SSEEvent SSE 事件（推送给前端）
type SSEEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Orchestrator OTACO 编排器
type Orchestrator struct {
	registry    *tools.Registry
	configMgr   *config.Manager
	questionMgr *tools.QuestionManager
	persister   *persister
	memorySvc   *memory.Service    // P2: 记忆服务（nil 时跳过记忆注入）
	extractor   *memory.Extractor // P2: 记忆自动提取器（nil 时跳过提取）
	approvalMgr *tools.ApprovalManager // P4: 审批管理器（写操作/危险工具审批流，nil 时拒绝需审批工具）
	skillMgr    *skill.Manager          // P4: Skill 管理器（nil 时跳过 Skill 按需加载）
	skillReg    *skill.Registry         // P4: Skill 注册表（元信息注入 system prompt，nil 时跳过）
}

// NewOrchestrator 创建编排器
func NewOrchestrator(registry *tools.Registry, cfgMgr *config.Manager, qMgr *tools.QuestionManager, repos *storage.Repositories, memSvc *memory.Service, approvalMgr *tools.ApprovalManager, skillMgr *skill.Manager, skillReg *skill.Registry) *Orchestrator {
	o := &Orchestrator{
		registry:    registry,
		configMgr:  cfgMgr,
		questionMgr: qMgr,
		persister:  newPersister(repos),
		memorySvc:  memSvc,
		approvalMgr: approvalMgr,
		skillMgr:    skillMgr,
		skillReg:    skillReg,
	}
	if memSvc != nil {
		o.extractor = memory.NewExtractor(memSvc)
	}
	return o
}

// GetRegistry 获取工具注册表
func (o *Orchestrator) GetRegistry() *tools.Registry {
	return o.registry
}

// Run 启动 OTACO 循环，返回 SSE 事件 channel
// sessionID 为空时不持久化历史（无状态单轮对话）
// userID 用于加载用户档案（全局记忆必注），projectID 用于项目记忆 RAG 注入（均可空）
func (o *Orchestrator) Run(ctx context.Context, query string, apiKey string, sessionID string, userID string, projectID string) <-chan SSEEvent {
	ch := make(chan SSEEvent, 32)

	go func() {
		defer close(ch)

		// P4: 注入 userID 到 ctx，供工具 Handler 取用（WS 指令路由到对应用户的本地客户端）
		ctx = context.WithValue(ctx, tools.CtxKeyUserID, userID)

		log.Printf("[INFO][otaco] Run 启动 sessionID=%s userID=%s projectID=%s query=%q", sessionID, userID, projectID, query)

		// 解析 API key
		appCfg := o.configMgr.GetAppConfig()
		resolvedKey, err := llm.ResolveAPIKey(apiKey, appCfg.LLMAPIKey)
		if err != nil {
			log.Printf("[ERROR][otaco] ResolveAPIKey 失败: %v", err)
			ch <- errorEvent("missing_api_key", err.Error())
			return
		}

		provider := llm.NewDeepSeekProvider(resolvedKey, appCfg.LLMBaseURL, appCfg.LLMModel, appCfg.LLMChatPath)

		// 并发控制（仅持久化会话需要；匿名单轮不占用配额）
		// 用 userID（client_id）做并发计数 key，更贴合「按用户限流」语义；匿名时回退 sessionID
		concurrencyKey := userID
		if concurrencyKey == "" {
			concurrencyKey = sessionID
		}
		var release func()
		if concurrencyKey != "" {
			var ok bool
			release, ok = o.persister.acquireConcurrency(concurrencyKey)
			if !ok {
				log.Printf("[WARN][otaco] 并发超限拒绝请求 concurrencyKey=%s", concurrencyKey)
				ch <- errorEvent("too_many_concurrent", "当前并发数过多，请稍后再试")
				return
			}
			defer release()
		}

		// P2: 构建记忆注入块（user 档案 + 项目信息 + 项目记忆 top5）
		// 失败不阻断对话，仅日志告警
		var memoryBlock string
		if o.memorySvc != nil {
			mb, mbErr := o.memorySvc.BuildMemoryBlock(ctx, userID, projectID, query)
			if mbErr != nil {
				log.Printf("[orchestrator] 构建记忆块失败（跳过注入）: %v", mbErr)
			} else {
				memoryBlock = mb
			}
		}

		// 组装 system prompt（skill 注册表在此注入，静态，一次性构建）
		systemPrompt := o.buildSystemPrompt(memoryBlock)
		// toolDefs 在 OTACO 循环内每轮构建：load_skill 执行后下一轮需包含新加载的 skill 工具

		// 加载历史（如果有 sessionID）
	var messages []llm.Message
	baseRound := 0 // 历史已存在的最大轮次，新对话从 baseRound+1 开始接续
	if sessionID != "" {
		hist, mr, err := o.persister.loadHistory(ctx, sessionID, systemPrompt)
		if err != nil {
			log.Printf("[ERROR][otaco] 加载历史失败 sessionID=%s: %v", sessionID, err)
			ch <- errorEvent("load_history_failed", "加载历史对话失败: "+err.Error())
			return
		}
		if hist != nil {
			messages = hist
			baseRound = mr
		}
	}
		// 兜底：无历史或无 sessionID
		if messages == nil {
			messages = []llm.Message{
				{Role: llm.RoleSystem, Content: systemPrompt},
			}
		}
		// 追加本轮用户输入
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: query})

		// 计算上下文占比并注入 system prompt 末尾（KV Cache 友好：前缀不变，占比放末尾）
		maxCtxLen := 64000 // 默认上下文长度
		if projectID != "" {
			if p, perr := o.persister.repos.Project.GetByID(projectID, ""); perr == nil && p != nil && p.MaxContextLength > 0 {
				maxCtxLen = p.MaxContextLength
			}
		}
		estimator := llm.NewTokenEstimator()
		totalTokens := estimator.EstimateMessages(messages)
		pct := estimator.EstimatePercentage(totalTokens, maxCtxLen)
		if pct > 0 {
			ctxInfo := fmt.Sprintf("\n\n## 上下文状态\n当前 token: %d/%d (%.1f%%)", totalTokens, maxCtxLen, pct)
			// 追加到 system message 末尾（最易变，放末尾，KV Cache 友好）
			if len(messages) > 0 && messages[0].Role == llm.RoleSystem {
				messages[0].Content += ctxInfo
			}
			log.Printf("[INFO][otaco] 上下文占比 token=%d/%d (%.1f%%) sessionID=%s", totalTokens, maxCtxLen, pct, sessionID)
			// 超 90% 强制同步压缩（兜底，防止 LLM 未自主调用工具导致超长）
			if pct >= 90 && sessionID != "" {
				log.Printf("[WARN][otaco] 上下文超 90%%，强制同步压缩 sessionID=%s", sessionID)
				o.persister.maybeSummarize(sessionID, resolvedKey, provider)
				// 强制压缩后需要重新加载历史（摘要更新，窗口外消息已标记）
				// 简化：本轮不重载，下一轮自然加载新摘要
			}
		}

		// 首轮对话后异步生成标题
		if sessionID != "" {
			o.persister.maybeGenerateTitle(ctx, sessionID, query, resolvedKey, provider)
		}

		// 读取限额
	ruleBasic := o.configMgr.GetRuleBasic()
	maxIter := 15
	maxErrors := 5
	maxRetryPerTool := 2
	if ruleBasic != nil {
		if ruleBasic.Limits.MaxOTACOIterations > 0 {
			maxIter = ruleBasic.Limits.MaxOTACOIterations
		}
		if ruleBasic.Limits.MaxConsecutiveErrors > 0 {
			maxErrors = ruleBasic.Limits.MaxConsecutiveErrors
		}
		if ruleBasic.Limits.MaxRetryPerTool > 0 {
			maxRetryPerTool = ruleBasic.Limits.MaxRetryPerTool
		}
	}

	consecutiveErrors := 0
	toolRetryCount := map[string]int{} // 工具调用 ID -> 已重试次数
	// 历史消息快照栈（用于 rollback）
	var messageHistoryStack [][]llm.Message

	// 推送初始 Observation（观察用户输入）
	ch <- SSEEvent{Type: "observation", Data: map[string]any{
		"stage":     "observe",
		"decision":  "pass",
		"reason":    "接收用户输入，开始处理",
		"iteration": 0,
	}}

	// OTACO 循环
	for iteration := 0; iteration < maxIter; iteration++ {
		log.Printf("[INFO][otaco] 第 %d 轮迭代开始", iteration+1)
		// 保存当前消息快照（用于 rollback）
		snapshot := make([]llm.Message, len(messages))
		copy(snapshot, messages)
		messageHistoryStack = append(messageHistoryStack, snapshot)

		// 推送 think 阶段开始
		ch <- SSEEvent{Type: "thought", Data: map[string]any{
			"stage":     "think",
			"iteration": iteration + 1,
		}}

		// Think: 调用 LLM（流式输出 Observation + Thought）
		// P4: toolDefs 每轮重建 —— load_skill 执行后下一轮需包含新加载的 skill 工具
		toolDefs := o.buildTools(sessionID)
		req := llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		}

		log.Printf("[INFO][otaco] LLM 调用开始 iteration=%d", iteration+1)
		content, toolCalls, finishReason := o.callLLMStream(ctx, provider, req, ch)
		log.Printf("[INFO][otaco] LLM 调用返回 iteration=%d finishReason=%s contentLen=%d toolCalls=%d", iteration+1, finishReason, len(content), len(toolCalls))
		if finishReason == "error" {
			log.Printf("[ERROR][otaco] LLM 调用失败 iteration=%d", iteration+1)
			ch <- errorEvent("llm_error", "LLM 调用失败，请检查 API Key 和网络")
			return
		}

		// 解析 LLM 输出中的 [OBSERVE:xxx] 决策
		observeDecision, observeReason, cleanedContent := parseObserveMark(content)

		// 记录 assistant 消息（用清理后的内容）
		assistantMsg := llm.Message{Role: llm.RoleAssistant, Content: cleanedContent}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		messages = append(messages, assistantMsg)

		// Check: 检测 [FINAL_ANSWER]
		if finalAnswer, found := detectFinalAnswer(cleanedContent); found {
			log.Printf("[INFO][otaco] 最终答案生成 iteration=%d", iteration+1)
			// 推送最后一轮 Observation（pass，准备输出）
			ch <- SSEEvent{Type: "observation", Data: map[string]any{
				"stage":     "observe",
				"decision":  "pass",
				"reason":    "已获取所有需要的信息",
				"iteration": iteration + 1,
			}}
			ch <- SSEEvent{Type: "output", Data: map[string]any{
				"stage":     "output",
				"iteration": iteration + 1,
				"reason":    "输出最终回答",
			}}
			// 持久化最终轮（首轮直接回答时也要保存 user 消息）
			if sessionID != "" {
				queryToSave := ""
				if iteration == 0 {
					queryToSave = query
				}
				obs := &observeInfo{decision: "pass", reason: "已获取所有需要的信息"}
				if err := o.persister.saveRound(sessionID, baseRound+iteration+1, queryToSave, obs, cleanedContent, toolCalls, nil, finalAnswer, resolvedKey, provider); err != nil {
				log.Printf("[ERROR][otaco] 保存最终轮失败 sessionID=%s round=%d: %v", sessionID, baseRound+iteration+1, err)
			}
			}

			// P2: 记忆自动提取（异步，不阻断对话返回）
			// - 关键词即时提取（每轮）：jieba + TF-IDF
			// - LLM 深度提取（累计5轮）：结构化 JSON
			// 提取失败仅 log，不影响对话
			if o.extractor != nil && sessionID != "" && userID != "" && projectID != "" {
				round := baseRound + iteration + 1
				log.Printf("[INFO][otaco] 异步记忆提取启动 projectID=%s userID=%s round=%d", projectID, userID, round)
				go func(q, ans string) {
					bgCtx := context.Background()
					o.extractor.ExtractFromTurn(bgCtx, projectID, userID, sessionID, round, q, ans)
					o.extractor.MaybeLLMExtract(bgCtx, projectID, userID, sessionID, provider)
				}(query, finalAnswer)
			}

			ch <- SSEEvent{Type: "done", Data: map[string]any{"answer": finalAnswer}}
			return
		}

		// 处理 rollback 决策（在 Action 前推送 Observation）
		if observeDecision == "rollback" {
			log.Printf("[WARN][otaco] rollback 触发 iteration=%d reason=%s", iteration+1, observeReason)
			ch <- SSEEvent{Type: "observation", Data: map[string]any{
				"stage":     "observe",
				"decision":  "rollback",
				"reason":    observeReason,
				"iteration": iteration + 1,
			}}
			// 持久化 rollback 轮
			if sessionID != "" {
				obs := &observeInfo{decision: "rollback", reason: observeReason}
				if err := o.persister.saveRound(sessionID, iteration+1, "", obs, cleanedContent, toolCalls, nil, "", resolvedKey, provider); err != nil {
				log.Printf("[ERROR][otaco] 保存 rollback 轮失败 sessionID=%s round=%d: %v", sessionID, iteration+1, err)
			}
			}
			// 回退到上一轮 Thought（弹出当前快照，恢复上一个）
			if len(messageHistoryStack) >= 2 {
				messageHistoryStack = messageHistoryStack[:len(messageHistoryStack)-1]
				messages = make([]llm.Message, len(messageHistoryStack[len(messageHistoryStack)-1]))
				copy(messages, messageHistoryStack[len(messageHistoryStack)-1])
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: fmt.Sprintf("[系统] 上一轮思路有误（%s），请换一种思路重新思考。", observeReason),
				})
			}
			continue
		}

		// Act: 执行工具
		hasToolError := false
		var toolResults []toolResultRecord // 用于持久化
		if len(toolCalls) > 0 {
			ch <- SSEEvent{Type: "thought", Data: map[string]any{"stage": "act"}}

			// retry 决策时检查重试次数
			if observeDecision == "retry" {
				for _, call := range toolCalls {
					toolRetryCount[call.ID]++
					if toolRetryCount[call.ID] > maxRetryPerTool {
						log.Printf("[ERROR][otaco] 达到最大重试 工具=%s 已重试=%d", call.Function.Name, maxRetryPerTool)
						ch <- errorEvent("max_retry", fmt.Sprintf("工具 %s 已重试 %d 次仍失败", call.Function.Name, maxRetryPerTool))
						return
					}
				}
			}

			results := o.processToolCalls(ctx, ch, toolCalls, sessionID, resolvedKey, provider)

		// Check: 检查结果
		ch <- SSEEvent{Type: "thought", Data: map[string]any{"stage": "check"}}
		for _, r := range results {
			eventData := map[string]any{
				"tool_name":   r.ToolName,
				"tool_call_id": r.ToolCallID,
			}
			rec := toolResultRecord{
				toolCallID: r.ToolCallID,
				toolName:   r.ToolName,
			}
			if r.Error != nil {
				log.Printf("[WARN][otaco] 工具执行失败 name=%s error=%v", r.ToolName, r.Error)
				hasToolError = true
				consecutiveErrors++
				eventData["content"] = ""
				eventData["error"] = r.Error.Error()
				ch <- SSEEvent{Type: "tool_result", Data: eventData}
				messages = append(messages, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: r.ToolCallID,
					Content:    fmt.Sprintf(`{"error":"%s"}`, escapeJSON(r.Error.Error())),
				})
				rec.errMsg = r.Error.Error()
			} else {
				log.Printf("[INFO][otaco] 工具执行成功 name=%s contentLen=%d", r.ToolName, len(r.Content))
				consecutiveErrors = 0
				eventData["content"] = r.Content
				ch <- SSEEvent{Type: "tool_result", Data: eventData}
				messages = append(messages, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: r.ToolCallID,
					Content:    r.Content,
				})
				rec.content = r.Content
			}
			toolResults = append(toolResults, rec)
		}

			if consecutiveErrors >= maxErrors {
				log.Printf("[ERROR][otaco] 达到最大错误数 consecutiveErrors=%d maxErrors=%d", consecutiveErrors, maxErrors)
				// 持久化失败轮（含工具结果）
				if sessionID != "" {
					obs := &observeInfo{decision: "retry", reason: fmt.Sprintf("连续 %d 轮失败", consecutiveErrors)}
					if err := o.persister.saveRound(sessionID, iteration+1, "", obs, cleanedContent, toolCalls, toolResults, "", resolvedKey, provider); err != nil {
				log.Printf("[ERROR][otaco] 保存失败轮失败 sessionID=%s round=%d: %v", sessionID, iteration+1, err)
			}
				}
				ch <- errorEvent("max_errors", fmt.Sprintf("连续 %d 轮工具执行失败，请稍后重试", maxErrors))
				return
			}
		} else {
			// 没有工具调用也没有 [FINAL_ANSWER]，提示 LLM
			messages = append(messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "请使用 [FINAL_ANSWER] 标记输出最终回答，或调用工具继续执行任务。",
			})
		}

		// 每轮 Check 后都推送 Observation（不会跳号）
		// 决策优先级：LLM 显式标记 > 规则推断
		obsDecision := observeDecision
		obsReason := observeReason
		if obsDecision == "" {
			// LLM 没输出标记，用规则推断
			if hasToolError {
				obsDecision = "retry"
				obsReason = "工具执行失败，下一轮重试"
			} else if len(toolCalls) > 0 {
				obsDecision = "pass"
				obsReason = "工具执行成功，继续下一步"
			} else {
				obsDecision = "pass"
				obsReason = "继续思考"
			}
		}
		ch <- SSEEvent{Type: "observation", Data: map[string]any{
			"stage":     "observe",
			"decision":  obsDecision,
			"reason":    obsReason,
			"iteration": iteration + 1,
		}}
		log.Printf("[INFO][otaco] observe 决策 iteration=%d decision=%s reason=%s", iteration+1, obsDecision, obsReason)

		// 持久化本轮 OTACO（含首轮用户输入）
		if sessionID != "" {
			queryToSave := ""
			if iteration == 0 {
				queryToSave = query
			}
			obs := &observeInfo{decision: obsDecision, reason: obsReason}
			if err := o.persister.saveRound(sessionID, baseRound+iteration+1, queryToSave, obs, cleanedContent, toolCalls, toolResults, "", resolvedKey, provider); err != nil {
			log.Printf("[ERROR][otaco] 保存轮次失败 sessionID=%s round=%d: %v", sessionID, baseRound+iteration+1, err)
		}
		}
		log.Printf("[INFO][otaco] 第 %d 轮迭代结束", iteration+1)
	}

	// 达到最大迭代次数（最后一轮已在循环底部持久化）
	log.Printf("[ERROR][otaco] 达到最大迭代次数 maxIter=%d", maxIter)
	ch <- errorEvent("max_iterations", fmt.Sprintf("已达到最大迭代次数 %d", maxIter))
	}()

	return ch
}

// callLLMStream 流式调用 LLM，实时推送 token 事件
func (o *Orchestrator) callLLMStream(ctx context.Context, provider llm.Provider, req llm.ChatRequest, ch chan<- SSEEvent) (string, []llm.ToolCall, string) {
	var contentBuilder strings.Builder
	var toolCalls []llm.ToolCall
	finishReason := ""

	streamCh := provider.ChatStream(ctx, req)

	for chunk := range streamCh {
		if chunk.FinishReason == "error" {
			return "", nil, "error"
		}

		// 推送增量文本到 thought 卡片（流式思考）
		if chunk.DeltaContent != "" {
			contentBuilder.WriteString(chunk.DeltaContent)
			ch <- SSEEvent{Type: "thought", Data: map[string]any{
				"stage":     "think",
				"content":   chunk.DeltaContent,
				"streaming": true,
			}}
		}

		if chunk.FinishReason != "" && chunk.FinishReason != "error" {
			finishReason = chunk.FinishReason
		}

		if len(chunk.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		}
	}

	return contentBuilder.String(), toolCalls, finishReason
}

// processToolCalls 处理工具调用（普通工具并行 + ask_user/summarize_history/load_skill 特殊处理 + skill 工具分流）
// sessionID/apiKey/provider 用于 summarize_history 工具执行压缩 + skill 工具按会话隔离执行
func (o *Orchestrator) processToolCalls(ctx context.Context, ch chan<- SSEEvent, calls []llm.ToolCall, sessionID string, apiKey string, provider llm.Provider) []tools.ToolResult {
	var normalCalls []llm.ToolCall        // 主 registry 工具（免审批 + 需审批）
	var skillCalls []llm.ToolCall         // P4: session 已加载的 skill 工具
	var results []tools.ToolResult

	// userID 从 ctx 取（load_skill 需按用户绑定工具 Handler）
	userID, _ := ctx.Value(tools.CtxKeyUserID).(string)

	for _, call := range calls {
		// 推送 tool_call 事件
		ch <- SSEEvent{Type: "tool_call", Data: map[string]any{
			"tool_name":   call.Function.Name,
			"tool_call_id": call.ID,
			"arguments":   parseArgsForDisplay(call.Function.Arguments),
		}}

		// 特殊工具优先判断（ask_user / summarize_history / load_skill）
		if call.Function.Name == "ask_user" {
			result := o.handleAskUser(ctx, ch, call)
			results = append(results, result)
			continue
		}
		if call.Function.Name == "summarize_history" {
			result := o.handleSummarizeHistory(ctx, ch, call, sessionID, apiKey, provider)
			results = append(results, result)
			continue
		}
		if call.Function.Name == "load_skill" {
			// P4: load_skill 特殊处理：按需加载 Skill 详细 schema，返回 Instructions 给 LLM
			result := o.handleLoadSkill(ctx, ch, call, sessionID, userID)
			results = append(results, result)
			continue
		}
		// 主 registry 工具
		if _, ok := o.registry.Get(call.Function.Name); ok {
			normalCalls = append(normalCalls, call)
			continue
		}
		// P4: session 已加载的 skill 工具
		if o.skillMgr != nil && o.skillMgr.HasTool(sessionID, call.Function.Name) {
			skillCalls = append(skillCalls, call)
			continue
		}
		// 未知工具，返回错误
		results = append(results, tools.ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Error:      fmt.Errorf("未知工具: %s", call.Function.Name),
		})
	}

	// P4: 分类主 registry 工具 —— 免审批（并行执行）vs 需审批（逐个审批后执行）
	// 审批流放执行层：写操作/危险工具在执行 Handler 前走审批（SSE waiting_approval），不占 LLM context
	var freeCalls, approvalCalls []llm.ToolCall
	for _, call := range normalCalls {
		tool, ok := o.registry.Get(call.Function.Name)
		log.Printf("[DEBUG][otaco] 分类工具 name=%s found=%v needsApproval=%v", call.Function.Name, ok, ok && tool.NeedsApproval)
		if ok && tool.NeedsApproval {
			approvalCalls = append(approvalCalls, call)
		} else {
			freeCalls = append(freeCalls, call)
		}
	}
	log.Printf("[DEBUG][otaco] 分类完成 freeCalls=%d approvalCalls=%d", len(freeCalls), len(approvalCalls))

	// 免审批工具并行执行
	if len(freeCalls) > 0 {
		freeResults := o.registry.ExecuteParallel(ctx, freeCalls)
		results = append(results, freeResults...)
	}

	// 需审批工具逐个审批后执行（审批是同步等待用户决定，不能并行）
	for _, call := range approvalCalls {
		approved, reason := o.requestApproval(ctx, ch, call)
		if !approved {
			errMsg := fmt.Sprintf("用户拒绝执行 %s", call.Function.Name)
			if reason != "" {
				errMsg += "（" + reason + "）"
			}
			results = append(results, tools.ToolResult{
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Error:      fmt.Errorf("%s", errMsg),
			})
			continue
		}
		// 审批通过，执行
		r := o.registry.ExecuteParallel(ctx, []llm.ToolCall{call})
		results = append(results, r...)
	}

	// P4: Skill 工具并行执行（按 session 隔离，走 skillMgr.ExecuteTool）
	// Skill 工具默认免审批；若需审批（如社交操作），由 Skill Handler 内部走 ask_user 或后续扩展
	if len(skillCalls) > 0 && o.skillMgr != nil {
		skillResults := o.executeSkillCalls(ctx, sessionID, skillCalls)
		results = append(results, skillResults...)
	}

	return results
}

// executeSkillCalls 并行执行 Skill 工具调用（session 级隔离）
func (o *Orchestrator) executeSkillCalls(ctx context.Context, sessionID string, calls []llm.ToolCall) []tools.ToolResult {
	log.Printf("[INFO][otaco] 并行执行 %d 个 skill tool_call sessionID=%s", len(calls), sessionID)
	var wg sync.WaitGroup
	results := make([]tools.ToolResult, len(calls))
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c llm.ToolCall) {
			defer wg.Done()
			results[idx] = o.skillMgr.ExecuteTool(ctx, sessionID, c)
		}(i, call)
	}
	wg.Wait()
	return results
}

// handleLoadSkill 处理 load_skill 工具调用
// LLM 调用 load_skill("knovis") → 系统按 session+userID 构建工具 → 返回 Instructions
// 下一轮 buildTools 会包含新加载的 skill 工具，LLM 可直接调用
func (o *Orchestrator) handleLoadSkill(ctx context.Context, ch chan<- SSEEvent, call llm.ToolCall, sessionID, userID string) tools.ToolResult {
	if o.skillMgr == nil {
		return tools.ToolResult{
			ToolCallID: call.ID,
			ToolName:   "load_skill",
			Error:      fmt.Errorf("Skill 管理器未配置"),
		}
	}
	log.Printf("[INFO][otaco] LLM 调用 load_skill sessionID=%s userID=%s", sessionID, userID)
	return o.skillMgr.HandleLoadSkill(ctx, call, sessionID, userID)
}

// requestApproval 请求用户审批工具执行
// 发送 SSE waiting_approval 事件，等待用户通过 POST /approval/:id/decide 提交决定
// 超时 5 分钟自动拒绝。审批流放执行层，不占 LLM context。
func (o *Orchestrator) requestApproval(ctx context.Context, ch chan<- SSEEvent, call llm.ToolCall) (bool, string) {
	log.Printf("[DEBUG][otaco] requestApproval 进入 tool=%s approvalMgr=%v", call.Function.Name, o.approvalMgr != nil)
	if o.approvalMgr == nil {
		// 审批管理器未配置，拒绝执行（安全优先）
		log.Printf("[WARN][otaco] requestApproval 审批管理器未配置，拒绝执行")
		return false, "审批管理器未配置"
	}
	approvalID := fmt.Sprintf("a_%d", time.Now().UnixNano())

	// 解析 args 摘要供前端展示
	argsDisplay := parseArgsForDisplay(call.Function.Arguments)

	log.Printf("[DEBUG][otaco] requestApproval 准备发送 waiting_approval 事件 id=%s tool=%s", approvalID, call.Function.Name)
	ch <- SSEEvent{Type: "waiting_approval", Data: map[string]any{
		"approval_id":  approvalID,
		"tool_name":    call.Function.Name,
		"tool_call_id": call.ID,
		"args":         argsDisplay,
	}}
	log.Printf("[DEBUG][otaco] requestApproval waiting_approval 已发送到 channel id=%s", approvalID)

	decisionCh := o.approvalMgr.Register(approvalID)
	select {
	case decision := <-decisionCh:
		log.Printf("[INFO][otaco] 审批决定 id=%s approved=%t reason=%s", approvalID, decision.Approved, decision.Reason)
		return decision.Approved, decision.Reason
	case <-ctx.Done():
		log.Printf("[WARN][otaco] 审批取消（请求结束）id=%s", approvalID)
		return false, "请求已取消"
	case <-time.After(5 * time.Minute):
		log.Printf("[WARN][otaco] 审批超时 id=%s", approvalID)
		return false, "审批超时（5分钟）"
	}
}

// handleAskUser 处理 ask_user 工具调用
func (o *Orchestrator) handleAskUser(ctx context.Context, ch chan<- SSEEvent, call llm.ToolCall) tools.ToolResult {
	result := tools.ToolResult{
		ToolCallID: call.ID,
		ToolName:   "ask_user",
	}

	// 解析参数
	args := map[string]any{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		log.Printf("[WARN][otaco] ask_user 参数解析失败: %v", err)
		result.Error = fmt.Errorf("ask_user 参数解析失败: %w", err)
		return result
	}

	questionsRaw, ok := args["questions"].([]any)
	if !ok || len(questionsRaw) == 0 {
		log.Printf("[WARN][otaco] ask_user 缺少 questions 参数")
		result.Error = fmt.Errorf("ask_user 缺少 questions 参数")
		return result
	}

	// 转换为 QuestionItem 列表
	var questions []tools.QuestionItem
	for _, qr := range questionsRaw {
		qm, ok := qr.(map[string]any)
		if !ok {
			continue
		}
		item := tools.QuestionItem{
			Question:    getString(qm, "question"),
			Header:      getString(qm, "header"),
			MultiSelect: getBool(qm, "multi_select"),
		}
		if opts, ok := qm["options"].([]any); ok {
			for _, opt := range opts {
				if om, ok := opt.(map[string]any); ok {
					item.Options = append(item.Options, tools.QuestionOption{
						Label:       getString(om, "label"),
						Description: getString(om, "description"),
					})
				}
			}
		}
		questions = append(questions, item)
	}

	if len(questions) == 0 {
		log.Printf("[WARN][otaco] ask_user 无有效问题")
		result.Error = fmt.Errorf("ask_user 无有效问题")
		return result
	}

	// 生成 question_id
	questionID := fmt.Sprintf("q_%d", time.Now().UnixNano())

	// 发送 waiting_question 事件
	payload := tools.QuestionPayload{
		QuestionID: questionID,
		Questions:  questions,
	}
	ch <- SSEEvent{Type: "waiting_question", Data: map[string]any{
		"question_id": questionID,
		"questions":   payload.Questions,
	}}

	// 注册并等待用户回答
	answerCh := o.questionMgr.Register(questionID)

	select {
	case answer := <-answerCh:
		// 用户已回答
		reply := answer.Reply
		if reply == "" {
			reply = formatAnswer(answer)
		}
		result.Content = reply
		return result
	case <-ctx.Done():
		log.Printf("[WARN][otaco] ask_user 用户取消 questionID=%s", questionID)
		result.Error = fmt.Errorf("用户未回答（请求已取消）")
		return result
	case <-time.After(5 * time.Minute):
		log.Printf("[WARN][otaco] ask_user 用户回答超时 questionID=%s", questionID)
		result.Error = fmt.Errorf("等待用户回答超时（5分钟）")
		return result
	}
}

// buildSystemPrompt 组装 system prompt
// memoryBlock 为 P2 记忆注入块（user 档案 + 项目信息 + 项目记忆 top5），插入到工具列表之后、当前时间之前
// 注入顺序（KV Cache 友好：稳定内容靠前，易变靠后）：
//   soul(稳定,可关) → rule_basic(稳定,必注) → OTACO流程(稳定) → 工具列表(稳定) → memoryBlock(易变) → 当前时间(最易变)
func (o *Orchestrator) buildSystemPrompt(memoryBlock string) string {
	var sb strings.Builder

	appCfg := o.configMgr.GetAppConfig()
	ruleBasic := o.configMgr.GetRuleBasic()

	// 人格设定（soul 默认注入，可由 MEMORY_INJECT_SOUL=false 关闭）
	if appCfg == nil || appCfg.MemoryInjectSoul {
		soul := o.configMgr.GetSoul()
		if soul != nil {
			sb.WriteString(fmt.Sprintf("你是 %s", soul.Identity.Name))
			if soul.Identity.Version != "" {
				sb.WriteString(fmt.Sprintf(" (v%s)", soul.Identity.Version))
			}
			sb.WriteString("。\n\n")

			if len(soul.Personality.Values) > 0 {
				sb.WriteString(fmt.Sprintf("性格特质：%s\n", strings.Join(soul.Personality.Values, "、")))
			}
			sb.WriteString(fmt.Sprintf("回复风格：%s\n\n", soul.Personality.Style))
		}
	}

	// 行为准则
	if ruleBasic != nil && len(ruleBasic.Rules) > 0 {
		sb.WriteString("## 行为准则\n")
		for _, rule := range ruleBasic.Rules {
			if rule.Enabled {
				sb.WriteString(fmt.Sprintf("- %s\n", rule.Description))
			}
		}
		sb.WriteString("\n")
	}

	// OTACO 工作流程
	sb.WriteString(`## 核心目标
你的首要任务是**回答用户的问题、帮助用户完成任务**。OTACO 循环只是你的工作方式，不是目的本身。
当用户问"你能做什么"、"你是谁"等关于你自身能力的问题时，基于下方的人格设定和可用工具，直接向用户介绍你的能力。

## 工作流程（OTACO 循环）
你使用 Observation-Think-Act-Check 循环工作，直到输出最终回答。

### 何时直接回答（无需工具）
如果用户的问题不需要调用工具（如自我介绍、知识问答、闲聊、解释说明），直接输出 [FINAL_ANSWER] 回答即可。
不需要输出 [OBSERVE:xxx] 标记，不需要调用工具，直接给出有价值的回答。

### 何时调用工具
如果用户的问题需要查询外部信息、执行操作、获取实时数据，才进入 OTACO 循环：

1. **Observation（观察）**：观察上一轮 Check 的结果（首轮观察用户输入），输出决策标记：
   - [OBSERVE:pass] 理由：结果正常，继续下一步
   - [OBSERVE:retry] 理由：结果异常，重试当前工具
   - [OBSERVE:rollback] 理由：思路错误，回退到上一轮重新思考
   首轮 Observation 直接输出 [OBSERVE:pass] 即可。

2. **Thought（思考）**：思考下一步行动，说明你的分析、决策、方案

3. **Action（执行）**：调用工具执行（同一轮多个工具会并行）

4. **Check（检查）**：自动检查工具执行结果（成功/失败）

### 调用工具时的输出格式
当需要调用工具时，每轮回复按顺序输出：[OBSERVE:xxx] → Thought → 调用工具
` + "```" + `
[OBSERVE:pass] 需要查询北京的天气信息
我先获取北京的经纬度，再查询天气...
（然后调用 geocode_city 工具）
` + "```" + `

## 最终回答
当你准备好给出最终回答时（无论是直接回答还是工具调用后的总结），在回答内容中包含 [FINAL_ANSWER] 标记：
` + "```" + `
[FINAL_ANSWER] 北京今天天气晴朗，气温 25°C...
` + "```" + `

## 工具调用规则
- 同一轮返回的多个工具调用会并行执行（无依赖时使用）
- 有依赖关系的工具（如先查经纬度再查天气）请分多轮调用
- 工具执行失败时，在下一轮 Observation 中判定 retry 或 rollback
- 需要用户提供更多信息时，调用 ask_user 工具提问

## ask_user 工具
当你需要用户补充信息、确认权限或选择路径时，调用 ask_user 工具。
支持一次提问多个问题（批量提问）。
`)

	// 工具列表
	toolList := o.registry.List()
	if len(toolList) > 0 {
		sb.WriteString("\n## 可用工具\n")
		for _, t := range toolList {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}

	// P4: Skill 注册表注入（工具列表之后、记忆块之前）
	// 顺序（KV Cache 友好）：soul → rule_basic → OTACO流程 → 工具列表 → skill 注册表 → memoryBlock → 当前时间
	// Skill 元信息每个约 25 tokens，详细 schema 由 load_skill 按需拉取
	if o.skillReg != nil {
		skillBlock := skill.BuildSkillRegistryBlock(o.skillReg.List())
		if skillBlock != "" {
			sb.WriteString(skillBlock)
		}
	}

	// P2: 记忆注入块（user 档案 + 项目信息 + 项目记忆 top5）
	// 放在工具列表之后、当前时间之前：工具列表稳定（KV Cache 命中），记忆块按查询变化
	if memoryBlock != "" {
		sb.WriteString("\n## 记忆上下文\n")
		sb.WriteString(memoryBlock)
	}

	// 当前时间放在 system prompt 最末尾：前缀（人格+规则+OTACO流程+工具）完全稳定，
	// 最大化命中 KV Cache；只有时间这小段每请求变化。
	// 显式使用 Asia/Shanghai 时区，与 .env 中 SESSION_TIMEZONE 保持一致。
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	sb.WriteString(fmt.Sprintf("\n## 当前时间\n%s（时区: Asia/Shanghai）\n",
		now.Format("2006-01-02 15:04:05 Monday")))

	return sb.String()
}

// buildTools 构建工具定义列表（含 ask_user + load_skill + session 已加载 skill 工具）
// sessionID 用于获取该会话已加载的 Skill 工具（load_skill 后，下一轮 tools 列表包含新工具）
// 每轮迭代调用：load_skill 执行后，下一轮 buildTools 会包含新加载的 skill 工具
func (o *Orchestrator) buildTools(sessionID string) []llm.ToolDefinition {
	defs := o.registry.ToDefinitions()
	// 追加 ask_user 工具定义
	defs = append(defs, tools.AskUserDefinition())
	// 追加 summarize_history 工具定义（LLM 自主调用压缩上下文）
	defs = append(defs, tools.SummarizeHistoryDefinition())
	// P4: 追加 load_skill 常驻 FC（Skill 按需加载入口）
	if o.skillMgr != nil {
		defs = append(defs, skill.LoadSkillDefinition())
		// 追加 session 已加载的 Skill 工具（load_skill 后常驻到对话结束）
		defs = append(defs, o.skillMgr.GetLoadedToolDefs(sessionID)...)
	}
	return defs
}

// handleSummarizeHistory 处理 summarize_history 工具调用
// LLM 自主调用（上下文占比超 80% 时），触发异步压缩并返回状态
// 压缩范围：窗口外未压缩消息 + 旧摘要合并重压
// 压缩是异步的，LLM 收到"已触发"后继续，下一轮加载新摘要
func (o *Orchestrator) handleSummarizeHistory(ctx context.Context, ch chan<- SSEEvent, call llm.ToolCall, sessionID string, apiKey string, provider llm.Provider) tools.ToolResult {
	result := tools.ToolResult{
		ToolCallID: call.ID,
		ToolName:   "summarize_history",
	}

	if sessionID == "" {
		result.Error = fmt.Errorf("无 sessionID，无法压缩历史")
		return result
	}

	// 解析参数（reason 可选，仅用于日志）
	args := map[string]any{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		log.Printf("[WARN][otaco] summarize_history 参数解析失败: %v", err)
	}
	reason := ""
	if r, ok := args["reason"].(string); ok {
		reason = r
	}
	log.Printf("[INFO][otaco] LLM 自主调用 summarize_history sessionID=%s reason=%s", sessionID, reason)

	// 触发异步压缩（不阻塞 OTACO 循环，下一轮加载新摘要）
	o.persister.maybeSummarize(sessionID, apiKey, provider)

	// 返回状态给 LLM（摘要内容走 system prompt，工具只返回状态）
	result.Content = `{"status":"triggered","message":"历史对话压缩已触发，下一轮对话将加载压缩后的摘要。当前对话可继续，无需等待。"}`
	return result
}

// detectFinalAnswer 检测 [FINAL_ANSWER] 标记
func detectFinalAnswer(content string) (string, bool) {
	re := regexp.MustCompile(`(?s)\[FINAL_ANSWER\]\s*(.*)`)
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1]), true
	}
	return "", false
}

// parseObserveMark 解析 [OBSERVE:pass/retry/rollback] 标记
// 返回：决策（pass/retry/rollback）、理由、清理后的内容
func parseObserveMark(content string) (string, string, string) {
	// 匹配 [OBSERVE:pass] 理由 或 [OBSERVE:retry] 理由 等
	re := regexp.MustCompile(`(?s)\[OBSERVE:(pass|retry|rollback)\]\s*([^\n]*)`)
	matches := re.FindStringSubmatch(content)
	if len(matches) >= 3 {
		decision := matches[1]
		reason := strings.TrimSpace(matches[2])
		// 从内容中移除 OBSERVE 标记行
		cleaned := re.ReplaceAllString(content, "")
		cleaned = strings.TrimSpace(cleaned)
		return decision, reason, cleaned
	}
	return "", "", content
}

// formatAnswer 格式化用户回答
func formatAnswer(answer tools.Answer) string {
	parts := []string{}
	if len(answer.SelectedLabels) > 0 {
		parts = append(parts, "选择: "+strings.Join(answer.SelectedLabels, ", "))
	}
	if answer.FreeText != "" {
		parts = append(parts, "补充: "+answer.FreeText)
	}
	if len(parts) == 0 {
		return "（用户未提供有效回答）"
	}
	return strings.Join(parts, " | ")
}

// parseArgsForDisplay 解析参数用于前端展示
func parseArgsForDisplay(argsStr string) any {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return argsStr
	}
	return args
}

func errorEvent(code, message string) SSEEvent {
	return SSEEvent{Type: "error", Data: map[string]any{"code": code, "message": message}}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func escapeJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return strings.Trim(string(b), "\"")
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
