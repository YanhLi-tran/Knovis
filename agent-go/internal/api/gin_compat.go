package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

// CtxKey context 键类型（避免 key 冲突）
type CtxKey string

// H map 简写（替代 gin.H，handler 响应体零改动迁移）
type H = map[string]any

const (
	ctxUserID   CtxKey = "user_id"
	ctxAuthType CtxKey = "auth_type"
	ctxClientID CtxKey = "client_id"
	ctxOwnKey   CtxKey = "using_own_key"
)

// GinCompat 兼容层：提供与 gin.Context 等价的常用方法，
// 底层基于标准库 http.ResponseWriter / *http.Request 实现。
// 目的：handler 业务逻辑零改动迁移（仅函数签名从 *gin.Context 改为 *GinCompat）。
type GinCompat struct {
	W   http.ResponseWriter
	R   *http.Request
	ctx context.Context
}

// NewGinCompat 基于标准库请求构造兼容上下文
func NewGinCompat(w http.ResponseWriter, r *http.Request) *GinCompat {
	return &GinCompat{W: w, R: r, ctx: r.Context()}
}

// ==================== 响应 ====================

// Writer 返回底层 ResponseWriter（兼容 gin c.Writer，SSE/WS 场景使用）
func (g *GinCompat) Writer() http.ResponseWriter {
	return g.W
}

// JSON 写入 JSON 响应（等价 gin c.JSON）
func (g *GinCompat) JSON(code int, obj any) {
	g.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	g.W.WriteHeader(code)
	_ = json.NewEncoder(g.W).Encode(obj)
}

// Header 设置响应头（等价 gin c.Header）
func (g *GinCompat) Header(key, value string) {
	g.W.Header().Set(key, value)
}

// SetHeader 设置响应头（gin 无此方法，兼容语义）
func (g *GinCompat) SetHeader(key, value string) {
	g.W.Header().Set(key, value)
}

// AbortWithStatusJSON 中止并写入 JSON（等价 gin c.AbortWithStatusJSON）
func (g *GinCompat) AbortWithStatusJSON(code int, obj any) {
	g.JSON(code, obj)
}

// AbortWithStatus 中止并写入状态码（等价 gin c.AbortWithStatus）
func (g *GinCompat) AbortWithStatus(code int) {
	g.W.WriteHeader(code)
}

// ==================== 请求 ====================

// Param 获取路径参数（等价 gin c.Param，需路由注册时保留 :name 语法）
// go-zero rest 路径参数通过 pathvar.Vars(r) 暴露（基于 httprouter）
func (g *GinCompat) Param(key string) string {
	return pathvar.Vars(g.R)[key]
}

// Query 获取查询参数（等价 gin c.Query）
func (g *GinCompat) Query(key string) string {
	return g.R.URL.Query().Get(key)
}

// GetHeader 获取请求头（等价 gin c.GetHeader）
func (g *GinCompat) GetHeader(key string) string {
	return g.R.Header.Get(key)
}

// FormFile 获取上传文件（等价 gin c.FormFile）
func (g *GinCompat) FormFile(name string) (*multipart.FileHeader, error) {
	if g.R.MultipartForm == nil {
		if err := g.R.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
	}
	f, fh, err := g.R.FormFile(name)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return fh, nil
}

// ClientIP 获取客户端 IP（等价 gin c.ClientIP）
func (g *GinCompat) ClientIP() string {
	// 优先 X-Forwarded-For / X-Real-IP（代理场景），回退 RemoteAddr
	if xff := g.R.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xr := g.R.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host := g.R.RemoteAddr
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	return host
}

// ==================== Body ====================

// ShouldBindJSON 绑定 JSON 请求体（等价 gin c.ShouldBindJSON）
func (g *GinCompat) ShouldBindJSON(obj any) error {
	if g.R.Body == nil {
		return errors.New("请求体为空")
	}
	body, err := io.ReadAll(g.R.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("请求体为空")
	}
	if err := json.Unmarshal(body, obj); err != nil {
		return err
	}
	return nil
}

// ==================== Context 值传递 ====================

// Set 注入值到 context（等价 gin c.Set，通过 request context 传递）
func (g *GinCompat) Set(key string, value any) {
	g.ctx = context.WithValue(g.ctx, CtxKey(key), value)
}

// Get 获取 context 值（等价 gin c.Get）
func (g *GinCompat) Get(key string) (any, bool) {
	v := g.ctx.Value(CtxKey(key))
	return v, v != nil
}

// GetString 获取 context 字符串值（等价 gin c.GetString）
func (g *GinCompat) GetString(key string) string {
	if v, ok := g.Get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ==================== 请求上下文 ====================

// Request 返回底层请求（带注入值的 context）
func (g *GinCompat) Request() *http.Request {
	return g.R.WithContext(g.ctx)
}

// requestContext 返回带注入值的 context
func (g *GinCompat) requestContext() context.Context {
	return g.ctx
}
