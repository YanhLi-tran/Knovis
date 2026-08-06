package skills

import (
	"context"
	"fmt"
	"log"
	"strings"

	"agent-go/internal/llm"
	"agent-go/internal/rag"
	"agent-go/internal/tools"
	"agent-go/internal/tools/skill"
)

// KBSummarySkillName Skill 唯一标识（与 skills/kb_summary/SKILL.md 的 name 一致）
const KBSummarySkillName = "kb_summary"

// BuildKBListDocs 构建 kb_list_docs 工具（文件型 skill kb_summary 的内置 Go 工具）
// kb_summary 由 SKILL.md 驱动（skills/kb_summary/SKILL.md）：正文定义总结流程，
// 本工具负责"确定总结范围"（列文档），内容检索复用常驻 FC 工具 rag_search。
// main.go 通过 skillReg.AttachToolBuilders 附加到已从 SKILL.md 加载的 kb_summary 定义上。
func BuildKBListDocs(docClient *rag.DocClient) skill.ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		return llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "kb_list_docs",
				Description: "列出企业知识库中的文档（可按公司 company_code、状态 status 过滤），返回 doc_id 等元信息。用于确定总结范围：先查看有哪些文档，再按公司/指定文档总结；检索时把得到的 doc_id 传给 rag_search 的 doc_ids 参数以限定范围。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"company_code": map[string]any{
							"type":        "string",
							"description": "可选：公司代码（如 wly），不传则列出全部公司的文档",
						},
						"status": map[string]any{
							"type":        "string",
							"description": "可选：文档状态，默认 completed（只列处理完成的文档）",
						},
					},
				},
			},
		}, func(ctx context.Context, args map[string]any) (string, error) {
			companyCode, _ := args["company_code"].(string)
			status := "completed"
			if s, ok := args["status"].(string); ok && s != "" {
				status = s
			}
			docs, err := docClient.ListDocuments(ctx, status, companyCode)
			if err != nil {
				log.Printf("[WARN][skill] kb_list_docs 失败 err=%v", err)
				return fmt.Sprintf("【文档列表】获取失败：%s", err.Error()), nil
			}
			if len(docs) == 0 {
				return "【文档列表】知识库中暂无符合条件的文档。", nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("【文档列表】共 %d 篇（company_code=%s, status=%s）\n", len(docs), orDash(companyCode), status))
			sb.WriteString("请把 doc_id 传给 rag_search 的 doc_ids 参数以限定检索范围。\n\n")
			for i, d := range docs {
				sb.WriteString(fmt.Sprintf("%d. doc_id=%d | %s | 公司:%s(%s) | %d年 %s | %d页 %d块\n",
					i+1, d.ID, d.Filename, d.CompanyName, d.CompanyCode, d.ReportYear, d.ReportType, d.TotalPages, d.TotalChunks))
			}
			return sb.String(), nil
		}, nil
	}
}

// orDash 空字符串显示为 "-"
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
