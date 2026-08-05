package ws

import (
	"log"
	"net/http"
	"strings"

	"agent-go/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Handler WebSocket 升级处理器（/ws/agent 端点）
// 鉴权策略与 AuthMiddleware 一致：
//   - JWT 优先：query param token（浏览器 WebSocket 无法设 Authorization header，前端在 URL 传 token）
//   - dev 模式降级：X-Client-ID header
//
// 安全提示：token 出现在 URL 可能被日志/代理记录，生产环境应改用「短期 ticket 交换」机制
// （前端先 POST /ws/ticket 换取一次性 ticket，再用 ticket 连 WS）。demo 阶段直接用 token。
type Handler struct {
	hub         *Hub
	authService *auth.AuthService
	authMode    string // strict / dev
	upgrader    websocket.Upgrader
}

// NewHandler 创建 WebSocket 升级处理器
func NewHandler(hub *Hub, authService *auth.AuthService, authMode string) *Handler {
	return &Handler{
		hub:         hub,
		authService: authService,
		authMode:    authMode,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Origin 校验：dev 模式放行所有源；生产环境应配置白名单
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

// HandleWS 处理 WebSocket 升级请求
// 路由：GET /ws/agent?token=<jwt_access_token>
func (h *Handler) HandleWS(c *gin.Context) {
	userID := h.authenticate(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少有效鉴权"})
		return
	}

	wsConn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[WARN][ws] 升级失败 userID=%s err=%v", userID, err)
		return
	}

	conn := newClientConn(userID, wsConn, h.hub)
	h.hub.Register(conn)

	// 启动读/写循环（任一退出都会触发 Close 清理）
	go conn.writeLoop()
	go conn.readLoop()

	log.Printf("[INFO][ws] 连接建立 userID=%s remote=%s", userID, c.ClientIP())
}

// authenticate 鉴权：优先 query token（JWT），dev 模式降级 X-Client-ID
func (h *Handler) authenticate(c *gin.Context) string {
	// 1) JWT via query param（WebSocket 标准做法）
	if token := strings.TrimSpace(c.Query("token")); token != "" && h.authService != nil {
		claims, err := h.authService.IsAccessTokenValid(c.Request.Context(), token)
		if err != nil {
			log.Printf("[WARN][ws] JWT 校验失败: %v", err)
			return ""
		}
		return claims.UserIDString()
	}
	// 2) dev 模式降级 X-Client-ID（过渡期兼容）
	if h.authMode == "dev" {
		if clientID := strings.TrimSpace(c.GetHeader("X-Client-ID")); clientID != "" {
			return clientID
		}
	}
	return ""
}

// GetHub 暴露 hub 供工具层使用（file/sandbox 工具通过 hub 下发指令）
func (h *Handler) GetHub() *Hub {
	return h.hub
}
