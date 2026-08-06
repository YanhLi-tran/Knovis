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

// KBSummarySkillName Skill 唯一标识
const KBSummarySkillName = "kb_summary"

// NewKBSummarySkillDefinition 创建企业知识库总结 Skill 定义
// 定位：用户要求对企业知识库内容做总结/概括时按需加载（低频复杂 → Skill，不常驻 context）。
//
// 设计（路线 2）：Skill 只提供"确定范围"的工具 kb_list_docs（列文档），
// 内容检索复用常驻 FC 工具 rag_search —— Instructions 引导 LLM：
//   1. kb_list_docs 拿 doc_id 列表（按公司/全库/指定文档）确定总结范围；
//   2. 分主题调用 rag_search（务必传 doc_ids 限定范围）逐主题检索；
//   3. 汇总为表格形式总结（贴合用户需求、标注来源、不臆想、缺口兜底）。
//
// 无需用户 token（doc-service 为内部服务），ToolBuilder 直接绑定 docClient。
func NewKBSummarySkillDefinition(docClient *rag.DocClient) *skill.SkillDefinition {
	instructions := `企业知识库总结工具已加载。总结流程：
1. 先调用 kb_list_docs 查看文档列表，确定总结范围（全库 / 某公司 company_code / 指定文档），并记录目标 doc_id。
2. 将总结目标拆分为主题（如：财务数据、业务结构、风险事项、战略规划），对每个主题调用 rag_search 检索；务必传入 kb_list_docs 得到的 doc_ids 限定范围，确保内容来自目标文档。
3. 汇总检索结果，以表格形式输出总结。

输出要求（严格遵守）：
- 用表格呈现总结；表格的列/行结构由你根据内容自行设计，但必须贴合用户的具体需求（如按公司、按年度、按主题、按文档）。
- 内容简洁明了，要点尽量带数据；每个要点标注来源，格式：[来源: 文档名, p页码, 章节]。
- 只能基于检索结果总结，禁止臆想或编造；某主题检索不到内容时如实说明"未找到相关记录"。
- 用户有特殊规定（指定维度、篇幅、格式等）时，优先按用户要求输出，再套用表格形式。
- 信息不足无法完整总结时，说明缺口并兜底回复，不要强行填充。`

	return &skill.SkillDefinition{
		Metadata: skill.SkillMetadata{
			Name:        KBSummarySkillName,
			Description: "总结企业知识库内容（按公司/指定文档/全库范围，表格形式输出）",
			Trigger:     "用户要求总结/概括企业知识库、公司文档、指定文档的内容时",
		},
		Instructions: instructions,
		ToolBuilders: []skill.ToolBuilder{
			buildKBListDocs(docClient),
		},
	}
}

// ===== 工具：kb_list_docs（列出知识库文档，确定总结范围）=====

func buildKBListDocs(docClient *rag.DocClient) skill.ToolBuilder {
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
