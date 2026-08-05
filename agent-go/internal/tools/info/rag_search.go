package info

import (
	"context"
	"fmt"
	"log"
	"strings"

	"agent-go/internal/rag"
	"agent-go/internal/tools"
)

// ragSearch 调用 doc-service 执行 RAG 检索(企业文档库)
// 失败时返回错误信息给 LLM,不阻断对话(约束:RAG 检索失败不让对话崩溃)
func ragSearch(ctx context.Context, args map[string]any, docClient *rag.DocClient) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("缺少参数 query")
	}

	topK := 5
	if v, ok := args["top_k"].(float64); ok && v > 0 {
		topK = int(v)
	}

	var docIDs []int
	if raw, ok := args["doc_ids"].([]any); ok && len(raw) > 0 {
		for _, v := range raw {
			if f, ok := v.(float64); ok {
				docIDs = append(docIDs, int(f))
			}
		}
	}

	resp, err := docClient.Search(ctx, query, topK, docIDs)
	if err != nil {
		log.Printf("[WARN][tools] rag_search 检索失败 query=%s err=%v(返回错误信息给 LLM,不阻断对话)", query, err)
		// 不返回 error,返回友好提示给 LLM,避免 OTACO 重试循环
		return fmt.Sprintf("【文档检索结果】检索失败:%s。可尝试直接回答或用其他工具。", err.Error()), nil
	}

	if len(resp.Results) == 0 {
		return fmt.Sprintf("【文档检索结果】未在文档库中找到与「%s」相关的内容。可尝试用 web_search 联网搜索,或直接回答。", query), nil
	}

	log.Printf("[INFO][tools] rag_search 成功 query=%s results=%d bm25=%d rag=%d elapsed=%dms",
		query, len(resp.Results), resp.BM25Count, resp.RAGCount, resp.ElapsedMs)

	return formatRAGResults(query, resp), nil
}

// formatRAGResults 格式化检索结果为带引用溯源的文本(供 LLM 引用)
func formatRAGResults(query string, resp rag.SearchResponse) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【文档检索结果】关键词:%s  共 %d 条(BM25召回:%d,向量召回:%d,耗时:%dms)\n",
		query, len(resp.Results), resp.BM25Count, resp.RAGCount, resp.ElapsedMs))
	sb.WriteString("\n请在最终回答中标注来源,格式:[来源: 文档名, p页码, 章节]\n\n")

	for i, r := range resp.Results {
		// 章节路径用 " > " 连接
		section := strings.Join(r.HeadingPath, " > ")
		if section == "" {
			section = "(无章节信息)"
		}
		// 命中路数
		sources := strings.Join(r.Sources, "+")
		if sources == "" {
			sources = "unknown"
		}
		fallbackTag := ""
		if r.Fallback {
			fallbackTag = " [段落超长,已返回命中块及相邻块]"
		}

		sb.WriteString(fmt.Sprintf("%d. 来源: %s | p%d | %s\n", i+1, r.DocName, r.PageNum, section))
		sb.WriteString(fmt.Sprintf("   (命中: %s | 相关度: %.2f%s)\n", sources, r.Score, fallbackTag))
		// 内容截断保护(单条过长时截断,避免 context 爆炸)
		content := r.Content
		if len([]rune(content)) > 1500 {
			content = string([]rune(content)[:1500]) + "...(截断)"
		}
		sb.WriteString("   内容: " + content + "\n\n")
	}
	return sb.String()
}

// RegisterRAGSearchTools 注册 RAG 文档检索工具(FC 常驻,与 web_search/file_read 同级)
// 注入位置:system prompt 工具列表(稳定区,KV Cache 友好)
func RegisterRAGSearchTools(registry *tools.Registry, docClient *rag.DocClient) {
	registry.Register(&tools.Tool{
		Name:        "rag_search",
		Description: "检索企业文档库(已上传的年报、政策、合同等 PDF),用于查询文档内的具体内容、数据、条款。返回带来源(文档名+页码+章节)的内容片段,便于引用溯源。当用户询问已上传文档中的信息时优先使用此工具。",
		Category:    "info",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "检索关键词或问题,例如「五粮液2023年营业收入」「贵州茅台毛利率」",
				},
				"top_k": map[string]any{
					"type":        "integer",
					"description": "返回结果数量,默认5",
					"default":     5,
				},
				"doc_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "可选:限定检索的文档ID范围,不传则检索全文档库",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return ragSearch(ctx, args, docClient)
		},
	})
}
