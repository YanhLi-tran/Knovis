package knovis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client Knovis REST API 客户端（用户/动态数据 owner）
// 供 /auth/me 透传用户资料与 knovis Skill 读工具复用，避免多处各写一份 HTTP 调用。
// 每次调用显式传入用户 Knovis token（Authorization: Bearer <token>），客户端本身不持有 token。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 创建 Knovis 客户端
// baseURL 形如 http://127.0.0.1:8080（由 KNOVIS_API_BASE_URL 注入）
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// GetUser 查询单个用户信息 GET /api/v1/users/:id
// 供 agent-go /auth/me 透传 Knovis 用户资料
func (c *Client) GetUser(ctx context.Context, token, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("userID 不能为空")
	}
	return c.do(ctx, token, http.MethodGet, "/api/v1/users/"+url.PathEscape(userID))
}

// GetProfile 查询用户资料 GET /api/v1/profile[/:user_id]
// userID 为空时查询当前登录用户（返回本人完整信息，含邮箱）
func (c *Client) GetProfile(ctx context.Context, token, userID string) (string, error) {
	path := "/api/v1/profile"
	if userID != "" {
		path += "/" + url.PathEscape(userID)
	}
	return c.do(ctx, token, http.MethodGet, path)
}

// GetFeed 动态流 GET /api/v1/feed?page=&page_size=
// page 从 1 开始，page_size 1-50（与 Knovis 分页一致）
func (c *Client) GetFeed(ctx context.Context, token string, page, pageSize int) (string, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 50 {
		pageSize = 50
	}
	path := fmt.Sprintf("/api/v1/feed?page=%d&page_size=%d", page, pageSize)
	return c.do(ctx, token, http.MethodGet, path)
}

// GetPost 动态详情 GET /api/v1/posts/:id（浏览数 +1）
func (c *Client) GetPost(ctx context.Context, token, postID string) (string, error) {
	if postID == "" {
		return "", fmt.Errorf("postID 不能为空")
	}
	return c.do(ctx, token, http.MethodGet, "/api/v1/posts/"+url.PathEscape(postID))
}

// do 发送 HTTP 请求（Bearer token 鉴权），统一处理 Knovis 错误响应
// Knovis 错误格式：{"code": <HTTP状态码>, "message": "..."}，解析 message 透出可读信息
func (c *Client) do(ctx context.Context, token, method, path string) (string, error) {
	urlStr := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 Knovis API 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode >= 400 {
		var er struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &er)
		if er.Message != "" {
			return "", fmt.Errorf("Knovis API 错误(%d): %s", resp.StatusCode, er.Message)
		}
		return "", fmt.Errorf("Knovis API 错误 status=%d", resp.StatusCode)
	}
	return string(body), nil
}
