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

	"agent-go/internal/trace"
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

// DocClient doc-service HTTP 客户端
type DocClient struct {
	baseURL string
	apiKey  string // P0-1: 子服务鉴权 key(X-API-Key 头)
	http    *http.Client
}

// NewDocClient 创建 doc-service 客户端
func NewDocClient(baseURL string, apiKey string) *DocClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8003"
	}
	return &DocClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 300 * time.Second, // 摄入大 PDF 可能慢
		},
	}
}

// setAuthHeaders 统一附加鉴权头(供 postJSON/GET/UploadFile 调用)
func (c *DocClient) setAuthHeaders(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey) // P0-1: 子服务鉴权
	}
}

// Search RAG 检索(BM25 + 向量融合 + 段落召回)
// userID 非空时通过 X-Owner-Id header 传给 doc-service 做文档权限隔离(全局共享+用户私有)
func (c *DocClient) Search(ctx context.Context, query string, topK int, docIDs []int, userID string) (SearchResponse, error) {
	body := map[string]any{
		"query":  query,
		"top_k":  topK,
	}
	if len(docIDs) > 0 {
		body["doc_ids"] = docIDs
	}
	var resp SearchResponse
	// 带_owner_id header 做权限隔离
	headers := map[string]string{}
	if userID != "" {
		headers["X-Owner-Id"] = userID
	}
	if err := c.postJSONWithHeaders(ctx, "/rag/search", body, &resp, headers); err != nil {
		return resp, err
	}
	log.Printf("[INFO][rag] POST /rag/search query=%s user=%s results=%d bm25=%d rag=%d elapsed=%dms",
		query, userID, len(resp.Results), resp.BM25Count, resp.RAGCount, resp.ElapsedMs)
	return resp, nil
}

// ListDocuments 文档列表(userID 非空时只返回全局共享+该用户私有文档)
func (c *DocClient) ListDocuments(ctx context.Context, status, companyCode, userID string) ([]Document, error) {
	u := fmt.Sprintf("%s/documents?status=%s&company_code=%s", c.baseURL, status, companyCode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(req)
	if userID != "" {
		req.Header.Set("X-Owner-Id", userID) // 文档权限隔离
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
// userID 非空时通过 X-Owner-Id 头传给 doc-service 做权限校验:
// 普通用户只能删自己的私有文档,全局共享文档(owner_id=NULL)禁删。
// userID 为空(管理员直连)可删任何文档。
func (c *DocClient) DeleteDocument(ctx context.Context, id uint, userID string) (DeleteResult, error) {
	u := fmt.Sprintf("%s/documents/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return DeleteResult{}, err
	}
	c.setAuthHeaders(req) // P0-1: 子服务鉴权
	if userID != "" {
		req.Header.Set("X-Owner-Id", userID) // 文档权限隔离:只能删自己的私有文档
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

// Scan 扫描本地目录导入(userID 空=全局共享,非空=用户私有)
func (c *DocClient) Scan(ctx context.Context, dirPath, userID string) (ScanResult, error) {
	var out ScanResult
	headers := map[string]string{}
	if userID != "" {
		headers["X-Owner-Id"] = userID
	}
	if err := c.postJSONWithHeaders(ctx, "/documents/scan", map[string]any{"dir_path": dirPath}, &out, headers); err != nil {
		return out, err
	}
	log.Printf("[INFO][rag] POST /documents/scan dir=%s user=%s total=%d success=%d failed=%d",
		dirPath, userID, out.Total, out.Success, out.Failed)
	return out, nil
}

// UploadFile 上传 PDF 文件(转发 multipart, userID 空=全局共享,非空=用户私有)
func (c *DocClient) UploadFile(ctx context.Context, filename string, fileData io.Reader, userID string) (map[string]any, error) {
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
	c.setAuthHeaders(req)
	if userID != "" {
		req.Header.Set("X-Owner-Id", userID) // 文档权限隔离
	}

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
	return c.postJSONWithHeaders(ctx, path, body, out, nil)
}

// postJSONWithHeaders 带额外 header 的 POST JSON(用于 X-Owner-Id 文档权限隔离)
func (c *DocClient) postJSONWithHeaders(ctx context.Context, path string, body any, out any, headers map[string]string) error {
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
	c.setAuthHeaders(req)
	for k, v := range headers {
		req.Header.Set(k, v)
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
