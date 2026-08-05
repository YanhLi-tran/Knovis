package api

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// uploadDocument 上传 PDF(转发到 doc-service /documents/ingest)
func (s *Server) uploadDocument(c *gin.Context) {
	if s.docClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "doc-service 未启用"})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少上传文件: " + err.Error()})
		return
	}
	if !strings.HasSuffix(strings.ToLower(file.Filename), ".pdf") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 PDF 文件"})
		return
	}

	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "打开文件失败: " + err.Error()})
		return
	}
	defer src.Close()

	result, err := s.docClient.UploadFile(c.Request.Context(), file.Filename, src)
	if err != nil {
		log.Printf("[ERROR][api] 上传文档失败 filename=%s: %v", file.Filename, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "doc-service 摄入失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] 上传文档成功 filename=%s", file.Filename)
	c.JSON(http.StatusOK, result)
}

// listDocuments 文档列表(转发 doc-service)
func (s *Server) listDocuments(c *gin.Context) {
	if s.docClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "doc-service 未启用"})
		return
	}
	status := c.Query("status")
	companyCode := c.Query("company_code")
	docs, err := s.docClient.ListDocuments(c.Request.Context(), status, companyCode)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询文档列表失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"documents": docs})
}

// deleteDocument 删除文档(级联,转发 doc-service)
func (s *Server) deleteDocument(c *gin.Context) {
	if s.docClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "doc-service 未启用"})
		return
	}
	id := parseUintParam(c, "id")
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文档 ID"})
		return
	}
	result, err := s.docClient.DeleteDocument(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "删除文档失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] 删除文档 id=%d chunks=%d vectors=%d", id, result.Chunks, result.Vectors)
	c.JSON(http.StatusOK, result)
}

// scanDocuments 扫描本地目录批量导入(转发 doc-service /documents/scan)
func (s *Server) scanDocuments(c *gin.Context) {
	if s.docClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "doc-service 未启用"})
		return
	}
	var req struct {
		DirPath string `json:"dir_path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 dir_path: " + err.Error()})
		return
	}
	result, err := s.docClient.Scan(c.Request.Context(), req.DirPath)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "扫描导入失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] 扫描导入完成 dir=%s total=%d success=%d failed=%d",
		req.DirPath, result.Total, result.Success, result.Failed)
	c.JSON(http.StatusOK, result)
}

// ragDebug 检索调试(返回各路召回数 + 融合明细 + 段落召回结果)
func (s *Server) ragDebug(c *gin.Context) {
	if s.docClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "doc-service 未启用"})
		return
	}
	var req struct {
		Query  string `json:"query" binding:"required"`
		TopK   int    `json:"top_k"`
		DocIDs []int  `json:"doc_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 query: " + err.Error()})
		return
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 5
	}
	resp, err := s.docClient.Search(c.Request.Context(), req.Query, topK, req.DocIDs)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "检索失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// parseUintParam 解析路径参数为 uint(失败返回 0)
func parseUintParam(c *gin.Context, key string) uint {
	idStr := c.Param(key)
	var id uint
	for _, ch := range idStr {
		if ch < '0' || ch > '9' {
			return 0
		}
		id = id*10 + uint(ch-'0')
	}
	return id
}
