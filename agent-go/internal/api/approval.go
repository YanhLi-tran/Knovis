package api

import (
	"net/http"

	"agent-go/internal/tools"
	"github.com/gin-gonic/gin"
)

// ApprovalDecisionRequest 用户审批决定请求体
// 前端收到 SSE waiting_approval 事件后，弹框让用户确认，调此接口提交决定
type ApprovalDecisionRequest struct {
	Approved bool   `json:"approved"` // true 批准执行 / false 拒绝执行
	Reason   string `json:"reason"`   // 拒绝原因（approved=false 时可选）
}

// decideApproval 用户提交工具执行审批决定
// 路由：POST /approval/:approval_id/decide
// 流程：前端收到 SSE waiting_approval 事件 → 弹框确认 → 调此接口 →
//
//	ApprovalManager.Submit 唤醒 OTACO 循环中等待的 requestApproval → 执行或跳过
func (s *Server) decideApproval(c *gin.Context) {
	approvalID := c.Param("approval_id")

	var req ApprovalDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误: " + err.Error()})
		return
	}

	decision := tools.ApprovalDecision{
		ApprovalID: approvalID,
		Approved:   req.Approved,
		Reason:     req.Reason,
	}

	if ok := s.approvalMgr.Submit(approvalID, decision); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "审批不存在或已过期", "approval_id": approvalID})
		return
	}

	// 审计日志：记录用户审批决定（resource=tool，覆盖 file_write/sandbox_exec 等需审批工具）
	userID := CurrentUserID(c)
	authType := c.GetString(CtxAuthType)
	action := "approve"
	if !req.Approved {
		action = "deny"
	}
	s.auditLogger.Log(userID, action, "tool", approvalID, c.ClientIP(), authType,
		gin.H{"reason": req.Reason})

	c.JSON(http.StatusOK, gin.H{
		"status":      "ok",
		"approval_id": approvalID,
		"approved":    req.Approved,
	})
}
