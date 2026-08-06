// Package rag 提供 doc-service(8003)的 HTTP 客户端,
// 供 rag_search FC 工具与文档管理 API 复用。
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"time"
)

// DocSearchResult 检索单条结果(与 doc-service RAGSearchResultItem 对齐)
type DocSearchResult struct {
	Content       string   `json:"content"`
	DocName       string   `json:"doc_name"`
	PageNum       int      `json:"page_num"`
	HeadingPath   []string `json:"heading_path"`
	SectionID     string   `json:"section_id"`
	ChunkIndex    int      `json:"chunk_index"`
	Score         float64  `json:"score"`
	Sources       []string `json:"sources"`
	Fallback      bool     `json:"fallback"`
	ContentLength int      `json:"content_length"`
}

// CandidateItem 融合前/后的候选条目(段落扩展前,与 doc-service CandidateItem 对齐)
// 用于评测 Recall@20 / MRR / 多路召回贡献度,非破坏性追加字段。
type CandidateItem struct {
	ChunkID     int      `json:"chunk_id"`
	DocumentID  int      `json:"document_id"`
	DocName     string   `json:"doc_name"`
	PageNum     int      `json:"page_num"`
	HeadingPath []string `json:"heading_path"`
	SectionID   string   `json:"section_id"`
	ChunkIndex  int      `json:"chunk_index"`
	Content     string   `json:"content"`
	Score       float64  `json:"score"`
	Sources     []string `json:"sources"`
	// RRF 改造后追加:各路原始分,供评测脚本做拒答判断(用 RAGRawScore 而非融合分)
	BM25RawScore float64 `json:"bm25_raw_score"`
	RAGRawScore  float64 `json:"rag_raw_score"`
}

// SearchStats 检索统计(调试/可观测用)
type SearchStats struct {
	BM25Count  int  `json:"bm25_count"`
	RAGCount   int  `json:"rag_count"`
	FusedCount int  `json:"fused_count"`
	Reranked   bool `json:"reranked"`
	ElapsedMs  int  `json:"elapsed_ms"`
	// 评测用:分阶段耗时(毫秒),非破坏性追加
	EmbedMs   int `json:"embed_ms"`
	BM25Ms    int `json:"bm25_ms"`
	RAGMs     int `json:"rag_ms"`
	FuseMs    int `json:"fuse_ms"`
	RerankMs  int `json:"rerank_ms"`
	SectionMs int `json:"section_ms"`
	TotalMs   int `json:"total_ms"`
}

// SearchResponse doc-service /rag/search 完整响应
type SearchResponse struct {
	Results []DocSearchResult `json:"results"`
	SearchStats
	// 评测用:融合前完整候选(段落扩展前 top-20),非破坏性追加
	FusedCandidates    []CandidateItem `json:"fused_candidates"`
	RerankedCandidates []CandidateItem `json:"reranked_candidates"`
	BM25Candidates     []CandidateItem `json:"bm25_candidates"`
	RAGCandidates      []CandidateItem `json:"rag_candidates"`
}

// Document 文档元信息(与 storage.Document 对齐,但用 JSON tag)
type Document struct {
	ID          uint   `json:"id"`
	Filename    string `json:"filename"`
	FilePath    string `json:"file_path"`
	FileSize    int64  `json:"file_size"`
	TotalPages  int    `json:"total_pages"`
	TotalChunks int    `json:"total_chunks"`
	Status      string `json:"status"`
	ErrorMsg    string `json:"error_msg"`
	CompanyCode string `json:"company_code"`
	CompanyName string `json:"company_name"`
	ReportYear  int    `json:"report_year"`
	ReportType  string `json:"report_type"`
	CreatedAt   string `json:"created_at"`
}

// ScanResult 扫描导入结果
type ScanResult struct {
	Total   int             `json:"total"`
	Success int             `json:"success"`
	Failed  int             `json:"failed"`
	Details []map[string]any `json:"details"`
}

// DeleteResult 删除结果
type DeleteResult struct {
	Deleted bool `json:"deleted"`
	Chunks  int  `json:"chunks"`
	Vectors int  `json:"vectors"`
}

// traceIDKey 是 context 中 trace_id 的 key 类型
type traceIDKeyType struct{}

var traceIDKey = traceIDKeyType{}

// WithTraceID 将 trace_id 注入 context，供下游 rag client 和 orchestrator 使用
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext 从 context 提取 trace_id，不存在则返回空串
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// DocClient doc-service HTTP 客户端
type DocClient struct {
	baseURL string
	http    *http.Client
}

// NewDocClient 创建 doc-service 客户端
func NewDocClient(baseURL string) *DocClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8003"
	}
	return &DocClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 300 * time.Second, // 摄入大 PDF 可能慢
		},
	}
}

// Search RAG 检索(BM25 + 向量融合 + 段落召回)
func (c *DocClient) Search(ctx context.Context, query string, topK int, docIDs []int) (SearchResponse, error) {
	body := map[string]any{
		"query":  query,
		"top_k":  topK,
	}
	if len(docIDs) > 0 {
		body["doc_ids"] = docIDs
	}
	var resp SearchResponse
	if err := c.postJSON(ctx, "/rag/search", body, &resp); err != nil {
		return resp, err
	}
	log.Printf("[INFO][rag] POST /rag/search query=%s results=%d bm25=%d rag=%d elapsed=%dms",
		query, len(resp.Results), resp.BM25Count, resp.RAGCount, resp.ElapsedMs)
	return resp, nil
}

// ListDocuments 文档列表
func (c *DocClient) ListDocuments(ctx context.Context, status, companyCode string) ([]Document, error) {
	u := fmt.Sprintf("%s/documents?status=%s&company_code=%s", c.baseURL, status, companyCode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 doc-service /documents 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("doc-service /documents 返回 HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Documents []Document `json:"documents"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	return out.Documents, nil
}

// DeleteDocument 删除文档(级联)
func (c *DocClient) DeleteDocument(ctx context.Context, id uint) (DeleteResult, error) {
	u := fmt.Sprintf("%s/documents/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return DeleteResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("调用 doc-service DELETE /documents/%d 失败: %w", id, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return DeleteResult{}, fmt.Errorf("doc-service DELETE 返回 HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var out DeleteResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return DeleteResult{}, fmt.Errorf("响应解析失败: %w", err)
	}
	return out, nil
}

// Scan 扫描本地目录导入
func (c *DocClient) Scan(ctx context.Context, dirPath string) (ScanResult, error) {
	var out ScanResult
	if err := c.postJSON(ctx, "/documents/scan", map[string]any{"dir_path": dirPath}, &out); err != nil {
		return out, err
	}
	log.Printf("[INFO][rag] POST /documents/scan dir=%s total=%d success=%d failed=%d",
		dirPath, out.Total, out.Success, out.Failed)
	return out, nil
}

// UploadFile 上传 PDF 文件(转发 multipart)
func (c *DocClient) UploadFile(ctx context.Context, filename string, fileData io.Reader) (map[string]any, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, fileData); err != nil {
		return nil, err
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/documents/ingest", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 doc-service /documents/ingest 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("doc-service /documents/ingest 返回 HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	return out, nil
}

// Health 健康检查
func (c *DocClient) Health(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// postJSON 通用 POST JSON
func (c *DocClient) postJSON(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("请求序列化失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if traceID, ok := ctx.Value(traceIDKey).(string); ok && traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("调用 doc-service %s 失败: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("doc-service %s 返回 HTTP %d: %s", path, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("响应解析失败: %w", err)
		}
	}
	return nil
}
