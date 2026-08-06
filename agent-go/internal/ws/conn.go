package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"agent-go/internal/trace"

	"github.com/gorilla/websocket"
)

var (
	// ErrConnClosed 连接已关闭
	ErrConnClosed = errors.New("connection closed")
	// ErrCommandTimeout 指令执行超时（客户端未在 timeout 内回传结果）
	ErrCommandTimeout = errors.New("command timeout")
)

const (
	defaultCommandTimeout = 30 * time.Second // 默认指令超时（与硬约束 sandbox 30s 对齐）
	heartbeatInterval     = 30 * time.Second // 服务端 ping 间隔
	writeWait             = 10 * time.Second // 单次写超时
	pongWait              = 75 * time.Second // 等 pong 超时（> heartbeat*2.5，容忍网络抖动）
	sendQueueSize         = 64               // 写队列缓冲（多并发指令下发时不阻塞）
)

// requestCounter 全局 request_id 计数器（配合时间戳保证唯一）
var requestCounter uint64

// ClientConn 单个本地客户端连接
// 生命周期：WebSocket 升级成功后创建，readLoop/writeLoop 退出时关闭。
// 并发模型：readLoop 单 goroutine 读 + 分发；writeLoop 单 goroutine 写；SendCommand 可多 goroutine 并发。
type ClientConn struct {
	userID    string
	ws        *websocket.Conn
	hub       *Hub
	send      chan []byte           // 写队列（writeLoop 消费）
	pendingMu sync.Mutex            // 保护 pending map
	pending   map[string]chan ResultPayload // request_id → result channel（SendCommand 等待结果用）
	done      chan struct{}         // 关闭信号
	closeOnce sync.Once             // 幂等关闭
	platform  string                // 客户端平台（register 声明）
	clientVer string                // 客户端版本（register 声明)
}

func newClientConn(userID string, ws *websocket.Conn, hub *Hub) *ClientConn {
	return &ClientConn{
		userID:  userID,
		ws:      ws,
		hub:     hub,
		send:    make(chan []byte, sendQueueSize),
		pending: make(map[string]chan ResultPayload),
		done:    make(chan struct{}),
	}
}

// readLoop 读循环：解析客户端消息（register/result/heartbeat/error），分发到对应处理
func (c *ClientConn) readLoop() {
	defer c.Close("read loop exit")
	c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		c.ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[WARN][ws] 读异常 userID=%s err=%v", c.userID, err)
			}
			return
		}
		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[WARN][ws] 消息解析失败 userID=%s err=%v", c.userID, err)
			continue
		}
		switch msg.Type {
		case MsgTypeRegister:
			c.platform = msg.Platform
			c.clientVer = msg.ClientVersion
			log.Printf("[INFO][ws] 收到 register userID=%s platform=%s version=%s caps=%v",
				c.userID, c.platform, c.clientVer, msg.Capabilities)
		case MsgTypeResult:
			c.dispatchResult(msg)
		case MsgTypeError:
			// 客户端错误报告，按 result(error) 分发到对应 pending
			log.Printf("[WARN][ws] 客户端报错 userID=%s requestID=%s err=%s", c.userID, msg.RequestID, msg.Error)
			c.dispatchResult(msg)
		case MsgTypeHeartbeat:
			// 客户端文本心跳，无需回复（服务端用 ping/pong 探活）
		default:
			log.Printf("[WARN][ws] 未知消息类型 userID=%s type=%s", c.userID, msg.Type)
		}
	}
}

// dispatchResult 分发结果到 pending channel（按 request_id 匹配）
func (c *ClientConn) dispatchResult(msg IncomingMessage) {
	c.pendingMu.Lock()
	ch, ok := c.pending[msg.RequestID]
	if ok {
		delete(c.pending, msg.RequestID)
	}
	c.pendingMu.Unlock()
	if !ok {
		log.Printf("[WARN][ws] 收到未知 requestID 的结果 userID=%s requestID=%s", c.userID, msg.RequestID)
		return
	}
	result := ResultPayload{Status: msg.Status, Result: msg.Result, Error: msg.Error}
	// status 为空时按 error 是否非空补全
	if result.Status == "" {
		if msg.Error != "" {
			result.Status = StatusError
		} else {
			result.Status = StatusSuccess
		}
	}
	select {
	case ch <- result:
	default:
		// channel 缓冲为 1，理论不会满；满了说明调用方已超时退出，丢弃
	}
}

// writeLoop 写循环：从 send 队列读消息写出 + 定时 ping 心跳
func (c *ClientConn) writeLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer func() {
		ticker.Stop()
		c.Close("write loop exit")
	}()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("[WARN][ws] 写失败 userID=%s err=%v", c.userID, err)
				return
			}
		case <-ticker.C:
			// gorilla 内置 ping，客户端应回 pong（SetPongHandler 已重置读超时）
			c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("[WARN][ws] ping 失败 userID=%s err=%v", c.userID, err)
				return
			}
		case <-c.done:
			return
		}
	}
}

// SendCommand 下发指令并等待结果（多 goroutine 并发安全）
// 流程：生成 request_id → 注册 pending channel → 发 send 队列 → 等待结果/超时/连接关闭
// 超时后清理 pending，防止内存泄漏（多用户高并发场景关键）
func (c *ClientConn) SendCommand(ctx context.Context, cmdType string, args map[string]any, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	requestID := newRequestID()
	resultCh := make(chan ResultPayload, 1)

	// 注册 pending
	c.pendingMu.Lock()
	c.pending[requestID] = resultCh
	c.pendingMu.Unlock()

	defer func() {
		// 确保清理 pending（超时/错误/正常返回均需清理，防泄漏）
		c.pendingMu.Lock()
		delete(c.pending, requestID)
		c.pendingMu.Unlock()
	}()

	out := OutgoingMessage{
		Type:        MsgTypeCommand,
		RequestID:   requestID,
		CommandType: cmdType,
		Args:        args,
		Timeout:     int(timeout.Seconds()),
		// 全链路 trace：透传 trace_id 到本地客户端（工具执行链路可追踪）
		TraceID: trace.TraceIDFromContext(ctx),
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal command: %w", err)
	}

	// 投递到写队列
	select {
	case c.send <- data:
	case <-c.done:
		return "", ErrConnClosed
	case <-time.After(writeWait):
		return "", fmt.Errorf("send queue full (client busy or slow)")
	}

	// 等待结果
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-resultCh:
		if res.Status == StatusError || res.Error != "" {
			return "", fmt.Errorf("client error: %s", res.Error)
		}
		return res.Result, nil
	case <-timer.C:
		return "", ErrCommandTimeout
	case <-c.done:
		return "", ErrConnClosed
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close 关闭连接（幂等，closeOnce 保证只执行一次清理）
// 清理动作：关闭 done → 发 close frame → 关 ws → 通知所有 pending 失败 → 从 hub 注销
func (c *ClientConn) Close(reason string) {
	c.closeOnce.Do(func() {
		log.Printf("[INFO][ws] 关闭连接 userID=%s reason=%s", c.userID, reason)
		close(c.done)
		// 发送 close frame（客户端可感知正常关闭）
		_ = c.ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason))
		_ = c.ws.Close()
		// 通知所有 pending 请求失败（避免调用方永久阻塞）
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			select {
			case ch <- ResultPayload{Status: StatusError, Error: "connection closed"}:
			default:
			}
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
		// 从 hub 注销（仅当仍是当前注册的连接才移除）
		c.hub.Unregister(c.userID, c)
	})
}

// newRequestID 生成全局唯一 request_id（时间戳 + 原子计数器）
func newRequestID() string {
	ts := time.Now().UnixNano()
	seq := atomic.AddUint64(&requestCounter, 1)
	return fmt.Sprintf("req_%d_%d", ts, seq)
}
