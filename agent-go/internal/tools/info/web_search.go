package info

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"agent-go/internal/tools"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	wsa "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/wsa/v20250508"
)

// searchResultItem 单条搜索结果（解析自 WSA Pages 的 JSON 字符串）
type searchResultItem struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Passage string  `json:"passage"`
	Content string  `json:"content"`
	Site    string  `json:"site"`
	Date    string  `json:"date"`
	Score   float64 `json:"score"`
}

// webSearch 调用腾讯云 WSA SearchPro 执行联网搜索
func webSearch(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("缺少参数 query")
	}

	secretID := os.Getenv("TENCENT_SECRET_ID")
	secretKey := os.Getenv("TENCENT_SECRET_KEY")
	if secretID == "" || secretKey == "" {
		return "", fmt.Errorf("未配置 TENCENT_SECRET_ID/TENCENT_SECRET_KEY，无法执行联网搜索")
	}

	// 限制单次搜索耗时
	searchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_ = searchCtx // SDK 调用为同步，context 仅作占位，后续 SDK 升级支持时接入

	cred := common.NewCredential(secretID, secretKey)
	cpf := profile.NewClientProfile()
	client, err := wsa.NewClient(cred, "", cpf)
	if err != nil {
		log.Printf("[ERROR][tools] WSA 客户端初始化失败 err=%v", err)
		return "", fmt.Errorf("WSA 客户端初始化失败: %w", err)
	}

	req := wsa.NewSearchProRequest()
	req.Query = &query

	resp, err := client.SearchPro(req)
	if err != nil {
		log.Printf("[ERROR][tools] WSA 搜索失败 query=%s err=%v", query, err)
		return "", fmt.Errorf("WSA 搜索失败: %w", err)
	}

	// resp.Response.Pages 是 []*string，每个元素是 JSON 字符串
	items := make([]searchResultItem, 0, len(resp.Response.Pages))
	for _, raw := range resp.Response.Pages {
		if raw == nil {
			continue
		}
		var it searchResultItem
		if err := json.Unmarshal([]byte(*raw), &it); err != nil {
			log.Printf("[WARN][tools] 搜索结果 JSON 解析失败(跳过该条) query=%s err=%v", query, err)
			continue
		}
		items = append(items, it)
	}

	// 最多保留前 5 条
	if len(items) > 5 {
		items = items[:5]
	}

	log.Printf("[INFO][tools] 联网搜索成功 query=%s result_count=%d", query, len(items))

	if len(items) == 0 {
		return fmt.Sprintf("【搜索结果】未找到与「%s」相关的内容", query), nil
	}

	return formatSearchResults(query, items), nil
}

// formatSearchResults 将搜索结果格式化为易读文本（供 LLM 引用）
func formatSearchResults(query string, items []searchResultItem) string {
	lines := []string{
		fmt.Sprintf("【搜索结果】关键词：%s  共 %d 条", query, len(items)),
		"",
	}
	for i, it := range items {
		header := fmt.Sprintf("%d. %s", i+1, it.Title)
		if it.Site != "" {
			header += "（" + it.Site + "）"
		}
		lines = append(lines, header)
		if it.URL != "" {
			lines = append(lines, "   链接: "+it.URL)
		}
		// 优先用 content（动态摘要，更完整），其次 passage
		summary := it.Content
		if summary == "" {
			summary = it.Passage
		}
		if summary != "" {
			lines = append(lines, "   摘要: "+summary)
		}
		if it.Date != "" {
			lines = append(lines, "   时间: "+it.Date)
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// RegisterWebSearchTools 注册联网搜索工具
func RegisterWebSearchTools(registry *tools.Registry) {
	registry.Register(&tools.Tool{
		Name:        "web_search",
		Description: "搜索互联网获取实时信息，用于查询新闻、最新资讯、行业动态、实时数据等需要联网的问题。输入搜索关键词，返回带标题、链接、摘要的搜索结果。",
		Category:    "info",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "搜索关键词，请提取用户问题中的核心关键词，例如「人工智能最新进展」「北京今日新闻」",
				},
			},
			"required": []string{"query"},
		},
		Handler: webSearch,
	})
}
