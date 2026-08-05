package api

import (
	"fmt"
	"net/http"

	"agent-go/internal/tools"
	"github.com/gin-gonic/gin"
)

// AnswerRequest 用户回答提问
type AnswerRequest struct {
	SelectedLabels []string `json:"selected_labels"`
	FreeText       string   `json:"free_text"`
}

// answerQuestion 用户回答 ask_user 的提问
func (s *Server) answerQuestion(c *gin.Context) {
	questionID := c.Param("question_id")

	var req AnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误: " + err.Error()})
		return
	}

	// 组合回复
	reply := ""
	if len(req.SelectedLabels) > 0 {
		reply += "选择: " + joinStrings(req.SelectedLabels, ", ")
	}
	if req.FreeText != "" {
		if reply != "" {
			reply += " | "
		}
		reply += "补充: " + req.FreeText
	}
	if reply == "" {
		reply = "（用户未提供有效回答）"
	}

	answer := tools.Answer{
		QuestionID:    questionID,
		SelectedLabels: req.SelectedLabels,
		FreeText:       req.FreeText,
		Reply:          reply,
	}

	if ok := s.questionMgr.Submit(questionID, answer); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在或已过期", "question_id": questionID})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"answer": gin.H{
			"reply":          reply,
			"selected_labels": req.SelectedLabels,
			"free_text":       req.FreeText,
		},
	})
}

func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

// 避免未使用 import
var _ = fmt.Sprintf
