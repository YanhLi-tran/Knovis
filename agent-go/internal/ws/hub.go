package ws

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// ErrClientOffline 客户端不在线（用户未启动本地 agent 或已断开）
var ErrClientOffline = errors.New("local agent offline")

// Hub 管理所有在线本地客户端连接（按 userID 隔离）
// 多用户高并发路由核心：userID → *ClientConn 一对一映射。
// 同一 userID 新连接踢旧连接（用户重启客户端或多设备登录时保证唯一活跃连接）。
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*ClientConn // userID → conn
}

// NewHub 创建连接管理器
func NewHub() *Hub {
	return &Hub{clients: make(map[string]*ClientConn)}
}

// Register 注册客户端连接；若同 userID 已有连接，踢掉旧连接（多设备登录场景）
func (h *Hub) Register(conn *ClientConn) {
	h.mu.Lock()
	old, ok := h.clients[conn.userID]
	h.clients[conn.userID] = conn
	h.mu.Unlock()
	if ok && old != conn {
		log.Printf("[WARN][ws] 同 userID 新连接踢旧连接 userID=%s", conn.userID)
		old.Close("replaced by new connection")
	}
	log.Printf("[INFO][ws] 客户端注册 userID=%s platform=%s online=%d", conn.userID, conn.platform, h.OnlineCount())
}

// Unregister 注销客户端连接（仅当 conn 仍是当前注册的那个才移除，防止误删新连接）
func (h *Hub) Unregister(userID string, conn *ClientConn) {
	h.mu.Lock()
	current, ok := h.clients[userID]
	if ok && current == conn {
		delete(h.clients, userID)
	} else {
		ok = false
	}
	h.mu.Unlock()
	if ok {
		log.Printf("[INFO][ws] 客户端注销 userID=%s online=%d", userID, h.OnlineCount())
	}
}

// Get 获取客户端连接
func (h *Hub) Get(userID string) (*ClientConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conn, ok := h.clients[userID]
	return conn, ok
}

// IsOnline 用户是否在线（本地 agent 是否连接）
func (h *Hub) IsOnline(userID string) bool {
	_, ok := h.Get(userID)
	return ok
}

// OnlineCount 在线客户端数（监控用）
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SendCommand 向指定用户的本地客户端下发指令并等待结果
// 多用户隔离：按 userID 路由，userID 无在线客户端返回 ErrClientOffline。
// 超时控制：timeout<=0 时用默认 30s；客户端超时或离线均返回明确 error 供 OTACO 决策。
// 并发安全：同一客户端可并行下发多个指令，按 request_id 区分结果。
func (h *Hub) SendCommand(ctx context.Context, userID string, cmdType string, args map[string]any, timeout time.Duration) (string, error) {
	conn, ok := h.Get(userID)
	if !ok {
		return "", ErrClientOffline
	}
	return conn.SendCommand(ctx, cmdType, args, timeout)
}
