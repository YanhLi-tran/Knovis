package tools

import (
	"log"
	"sync"
)

// ApprovalDecision 用户审批决定
type ApprovalDecision struct {
	ApprovalID string
	Approved   bool
	Reason     string
}

// ApprovalManager 审批管理器（复用 QuestionManager 模式）
// 审批流放执行层：需审批的工具（file_write / sandbox_exec 白名单外）在 processToolCalls
// 中先走审批（SSE waiting_approval → 等待用户决定），批准后才执行 Handler。
// 运行时拦截，不占 context（审批交互不进入 LLM 上下文）。
type ApprovalManager struct {
	mu      sync.Mutex
	pending map[string]chan ApprovalDecision
}

// NewApprovalManager 创建审批管理器
func NewApprovalManager() *ApprovalManager {
	return &ApprovalManager{pending: make(map[string]chan ApprovalDecision)}
}

// Register 注册一个审批，返回接收决定的 channel
func (am *ApprovalManager) Register(id string) chan ApprovalDecision {
	ch := make(chan ApprovalDecision, 1)
	am.mu.Lock()
	am.pending[id] = ch
	am.mu.Unlock()
	log.Printf("[INFO][approval] 注册审批 id=%s", id)
	return ch
}

// Submit 提交用户审批决定，返回是否成功
func (am *ApprovalManager) Submit(id string, decision ApprovalDecision) bool {
	am.mu.Lock()
	ch, ok := am.pending[id]
	if ok {
		delete(am.pending, id)
	}
	am.mu.Unlock()
	if !ok {
		log.Printf("[WARN][approval] 审批不存在或已过期 id=%s", id)
		return false
	}
	ch <- decision
	return true
}
