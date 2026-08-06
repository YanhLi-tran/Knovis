package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"agent-go/internal/config"
	"agent-go/internal/trace"
)

// MemoryClient Python 记忆子服务 HTTP 客户端（/embed + /search + /delete）
// 不可用时调用方需自行降级（retriever 回退 MySQL ListByProject）
type MemoryClient struct {
	baseURL string
	http    *http.Client
}

// NewMemoryClient 创建 HTTP 客户端
func NewMemoryClient(cfg *config.AppConfig) *MemoryClient {
	base := cfg.MemoryServiceURL
	if base == "" {
		base = "http://127.0.0.1:8002"
	}
	return &MemoryClient{
		baseURL: base,
		http: &http.Client{
			Timeout: 60 * time.Second, // embedding 首次加载模型可能慢
		},
	}
}

// EmbedItem 待 embedding 的记忆项（与 Python 侧 EmbedItem 对齐）
type EmbedItem struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	MemoryType  string `json:"memory_type"`
	Source      string `json:"source"`
	Importance  int    `json:"importance"`
}

type embedRequest struct {
	ProjectID string      `json:"project_id"`
	Items     []EmbedItem `json:"items"`
}

type embedResponse struct {
	Embedded int      `json:"embedded"`
	IDs      []string `json:"ids"`
}

// Embed 批量文本转向量 + upsert 到 Chroma
func (c *MemoryClient) Embed(ctx context.Context, projectID string, items []EmbedItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	log.Printf("[INFO][memory] POST /embed 开始 (project=%s, items=%d)", projectID, len(items))
	var resp embedResponse
	if err := c.post(ctx, "/embed", embedRequest{ProjectID: projectID, Items: items}, &resp); err != nil {
		return 0, err
	}
	log.Printf("[INFO][memory] POST /embed 完成 (project=%s, embedded=%d)", projectID, resp.Embedded)
	return resp.Embedded, nil
}

// SearchResult 检索单条结果（与 Python 侧 SearchResult 对齐）
type SearchResult struct {
	ID          string   `json:"id"`
	Content     string   `json:"content"`
	MemoryType  string   `json:"memory_type"`
	Source      string   `json:"source"`
	Importance  int      `json:"importance"`
	Score       float64  `json:"score"`
	Sources     []string `json:"sources"`
}

type searchRequest struct {
	ProjectID string `json:"project_id"`
	Query     string `json:"query"`
	TopK      int    `json:"top_k"`
}

type searchResponse struct {
	Results    []SearchResult `json:"results"`
	BM25Count  int            `json:"bm25_count"`
	RAGCount   int            `json:"rag_count"`
}

// Search 混合检索（BM25 + RAG 融合 top-K）
// 返回结果列表（如需 bm25_count/rag_count 观测字段，用 SearchWithStats）
func (c *MemoryClient) Search(ctx context.Context, projectID, query string, topK int) ([]SearchResult, error) {
	resp, err := c.SearchWithStats(ctx, projectID, query, topK)
	if err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// SearchWithStats 混合检索，返回完整响应（含 bm25_count/rag_count 观测字段）
func (c *MemoryClient) SearchWithStats(ctx context.Context, projectID, query string, topK int) (searchResponse, error) {
	if topK <= 0 {
		topK = 5
	}
	traceID := trace.TraceIDFromContext(ctx)
	log.Printf("[INFO][memory] POST /search 开始 (project=%s, topK=%d) trace=%s", projectID, topK, traceID)
	var resp searchResponse
	if err := c.post(ctx, "/search", searchRequest{ProjectID: projectID, Query: query, TopK: topK}, &resp); err != nil {
		return resp, err
	}
	log.Printf("[INFO][memory] POST /search 完成 (project=%s, results=%d, bm25=%d, rag=%d) trace=%s", projectID, len(resp.Results), resp.BM25Count, resp.RAGCount, traceID)
	return resp, nil
}

// Delete 删除 Chroma 向量
func (c *MemoryClient) Delete(ctx context.Context, projectID string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	log.Printf("[INFO][memory] POST /delete 开始 (project=%s, ids=%d)", projectID, len(ids))
	var resp struct {
		Deleted int `json:"deleted"`
	}
	if err := c.post(ctx, "/delete", map[string]any{"project_id": projectID, "ids": ids}, &resp); err != nil {
		return 0, err
	}
	log.Printf("[INFO][memory] POST /delete 完成 (project=%s, deleted=%d)", projectID, resp.Deleted)
	return resp.Deleted, nil
}

// Keyword 提取出的单个关键词
type Keyword struct {
	Word   string  `json:"word"`
	Weight float64 `json:"weight"`
}

type extractKeywordsRequest struct {
	Texts []string `json:"texts"`
	TopK  int      `json:"top_k"`
}

type extractKeywordsResponse struct {
	Keywords []Keyword `json:"keywords"`
}

// ExtractKeywords 关键词即时提取（jieba 分词 + TF-IDF）
// 每轮对话后异步调用，结果存入 agent_memories
func (c *MemoryClient) ExtractKeywords(ctx context.Context, texts []string, topK int) ([]Keyword, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	log.Printf("[INFO][memory] POST /extract_keywords 开始 (texts=%d, topK=%d)", len(texts), topK)
	var resp extractKeywordsResponse
	if err := c.post(ctx, "/extract_keywords", extractKeywordsRequest{Texts: texts, TopK: topK}, &resp); err != nil {
		return nil, err
	}
	log.Printf("[INFO][memory] POST /extract_keywords 完成 (keywords=%d)", len(resp.Keywords))
	return resp.Keywords, nil
}

// DeleteCollection 删除整个项目 collection
func (c *MemoryClient) DeleteCollection(ctx context.Context, projectID string) error {
	traceID := trace.TraceIDFromContext(ctx)
	log.Printf("[INFO][memory] DELETE /collection/%s 开始 trace=%s", projectID, traceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/collection/"+projectID, nil)
	if err != nil {
		return err
	}
	if traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("删除 collection 失败: HTTP %d", resp.StatusCode)
	}
	log.Printf("[INFO][memory] DELETE /collection/%s 完成 trace=%s", projectID, traceID)
	return nil
}

// Health 健康检查（探活 Python 子服务）
func (c *MemoryClient) Health(ctx context.Context) bool {
	log.Printf("[INFO][memory] GET /health 开始 trace=%s", trace.TraceIDFromContext(ctx))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	if traceID := trace.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	ok := resp.StatusCode == http.StatusOK
	log.Printf("[INFO][memory] GET /health 完成 (ok=%v, status=%d)", ok, resp.StatusCode)
	return ok
}

// post 通用 POST 请求（透传 X-Trace-Id 头，全链路 trace）
func (c *MemoryClient) post(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("请求序列化失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if traceID := trace.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		log.Printf("[ERROR][memory] HTTP 调用失败 %s: %v", path, err)
		return fmt.Errorf("调用记忆服务 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		log.Printf("[ERROR][memory] 记忆服务 %s 返回 HTTP %d: %s", path, resp.StatusCode, string(respBody))
		return fmt.Errorf("记忆服务 %s 返回 HTTP %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("响应解析失败: %w", err)
		}
	}
	return nil
}
