package api

import (
	"log"

	"agent-go/internal/audit"
	"agent-go/internal/auth"
	"agent-go/internal/config"
	"agent-go/internal/knovis"
	"agent-go/internal/memory"
	"agent-go/internal/orchestrator"
	"agent-go/internal/rag"
	"agent-go/internal/storage"
	"agent-go/internal/tools"
	"agent-go/internal/ws"

	"github.com/gin-gonic/gin"
)

// Server HTTP API 服务器
type Server struct {
	router          *gin.Engine
	orchestrator    *orchestrator.Orchestrator
	questionMgr     *tools.QuestionManager
	approvalMgr     *tools.ApprovalManager // P4: 审批管理器（file_write/sandbox_exec 等需审批工具）
	configMgr       *config.Manager
	repos           *storage.Repositories
	memorySvc       *memory.Service             // P2: 记忆服务（CRUD API + 注入）
	ttlScheduler    *memory.TTLScheduler        // P2: 记忆 TTL 定时任务（手动触发用）
	msgTtlScheduler *memory.MessageTTLScheduler // P2: 消息 TTL 定时任务（手动触发用）
	// P3: 鉴权
	authService *auth.AuthService
	authMode    string // strict / dev（过渡期 dev：JWT 优先 + X-Client-ID 降级）
	auditLogger  *audit.Logger  // P3: 审计日志（异步写）
	wsHandler    *ws.Handler    // P4: WebSocket 升级处理器（本地客户端连接，file/sandbox 工具通道）
	docClient    *rag.DocClient  // P5: doc-service 客户端(RAG 文档管理 + 检索)
	knovisClient *knovis.Client  // Knovis 客户端（/auth/me 透传用户资料）
}

// NewServer 创建 API 服务器
func NewServer(orch *orchestrator.Orchestrator, qMgr *tools.QuestionManager, cfgMgr *config.Manager, repos *storage.Repositories, memSvc *memory.Service, ttlSch *memory.TTLScheduler, msgTtlSch *memory.MessageTTLScheduler, authSvc *auth.AuthService, authMode string, wsHub *ws.Hub, approvalMgr *tools.ApprovalManager, docClient *rag.DocClient, knovisClient *knovis.Client) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// CORS 中间件（阶段一允许所有源）
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-LLM-API-Key, X-Client-ID")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	s := &Server{
		router:          r,
		orchestrator:    orch,
		questionMgr:     qMgr,
		approvalMgr:     approvalMgr,
		configMgr:       cfgMgr,
		repos:           repos,
		memorySvc:       memSvc,
		ttlScheduler:    ttlSch,
		msgTtlScheduler: msgTtlSch,
		authService:     authSvc,
		authMode:        authMode,
		auditLogger:     audit.NewLogger(repos.Audit),
		docClient:       docClient,
		knovisClient:    knovisClient,
	}
	// P4: 创建 WebSocket 处理器（复用 authService 鉴权）
	s.wsHandler = ws.NewHandler(wsHub, s.authService, s.authMode)

	// P3: 鉴权中间件（替代旧的 X-Client-ID 中间件）
	// 策略：strict 仅 JWT / dev JWT 优先 + X-Client-ID 降级
	// 白名单：/health /auth/* /static/* / 自动放行
	r.Use(s.AuthMiddleware())

	s.registerRoutes()
	log.Printf("[INFO][api] NewServer 创建成功 authMode=%s", authMode)
	return s
}

// registerRoutes 注册路由
func (s *Server) registerRoutes() {
	r := s.router

	// 健康检查
	r.GET("/health", s.health)

	// SSO 形态：注册/登录/刷新由 Knovis 提供，agent-go 仅保留 logout（公开）+ me + 凭证管理（需鉴权）
	authGroup := r.Group("/auth")
	{
		authGroup.POST("/logout", s.logout)
		authGroup.GET("/me", s.me) // /auth/me 需要鉴权（不在白名单），me 内透传 Knovis 用户资料
		authGroup.POST("/llm-key", s.setLLMKey)
		authGroup.GET("/llm-key", s.getLLMKey)
		authGroup.DELETE("/llm-key", s.deleteLLMKey)
		// P4: Knovis token 凭证管理（knovis Skill 读操作用）
		authGroup.POST("/knovis-token", s.setKnovisToken)
		authGroup.GET("/knovis-token", s.getKnovisToken)
		authGroup.DELETE("/knovis-token", s.deleteKnovisToken)
	}

	// 工具列表
	r.GET("/tools", s.listTools)

	// P4: WebSocket 端点（本地客户端连接，file/sandbox 工具通过此通道下发指令）
	// 鉴权由 ws.Handler 自行处理（query token），中间件白名单已放行 /ws/*
	r.GET("/ws/agent", s.wsHandler.HandleWS)

	// Session 管理（要求 X-Client-ID）
	sessions := r.Group("/sessions")
	{
		sessions.POST("", s.createSession)
		sessions.GET("", s.listSessions)
		sessions.GET("/:id", s.getSession)
		sessions.GET("/:id/messages", s.listSessionMessages) // 历史消息回放
		sessions.GET("/:id/messages/summarized", s.listSummarizedMessages) // 已压缩消息列表（前端恢复用）
		sessions.POST("/:id/messages/:mid/restore", s.restoreMessage)      // 恢复单条已压缩消息（重置 TTL）
		sessions.PATCH("/:id", s.updateSession)              // 重命名 / 置顶
		sessions.DELETE("/:id", s.deleteSession)
	}

	// P2: 项目管理 + 记忆系统
	projects := r.Group("/projects")
	{
		projects.POST("", s.createProject)
		projects.GET("", s.listProjects)
		projects.GET("/:id", s.getProject)
		projects.PATCH("/:id", s.updateProject)
		projects.DELETE("/:id", s.deleteProject)
		projects.GET("/:id/sessions", s.listProjectSessions) // 项目下的 session
		// 项目记忆 CRUD
		projects.POST("/:id/memories", s.createMemory)
		projects.GET("/:id/memories", s.listMemories)
		projects.POST("/:id/memories/embed", s.embedPending)        // 手动触发批量 embed
		projects.POST("/:id/memories/search", s.searchMemories)     // 检索测试
		projects.GET("/:id/memories/archived", s.listArchivedMemories) // 归档记忆列表
	}
	// 单条记忆操作（跨项目通用）
	r.PUT("/memory/memories/:id", s.updateMemory)
	r.DELETE("/memory/memories/:id", s.deleteMemory)
	r.POST("/memory/memories/:id/archive", s.archiveMemory)        // 归档单条记忆
	r.POST("/memory/archive/:id/restore", s.restoreMemory)        // 恢复归档记忆
	r.POST("/memory/ttl/run", s.triggerTTL)                       // 手动触发 TTL 归档（运维用）
	r.POST("/memory/message-ttl/run", s.triggerMessageTTL)        // 手动触发消息 TTL 软删（运维用）
	// 用户档案
	r.GET("/memory/user-config", s.getUserConfig)
	r.PUT("/memory/user-config", s.upsertUserConfig)

	// 对话流（SSE）
	r.POST("/chat/stream", s.chatStream)

	// 用户回答提问
	r.POST("/question/:question_id/answer", s.answerQuestion)

	// P4: 工具执行审批（file_write/sandbox_exec 等需审批工具，前端收到 waiting_approval SSE 后调此接口）
	r.POST("/approval/:approval_id/decide", s.decideApproval)

	// P5: RAG 文档管理 + 检索调试
	docs := r.Group("/documents")
	{
		docs.POST("/upload", s.uploadDocument) // 上传 PDF(转发 doc-service)
		docs.GET("", s.listDocuments)          // 文档列表
		docs.DELETE("/:id", s.deleteDocument)  // 删除文档(级联)
		docs.POST("/scan", s.scanDocuments)    // 扫描本地目录导入
	}
	r.POST("/rag/debug", s.ragDebug) // 检索调试(返回各路召回数 + 融合明细)

	// 静态文件服务（前端页面）
	r.Static("/static", "./static")
	r.StaticFile("/", "./static/index.html")

	log.Printf("[INFO][api] registerRoutes 路由注册完成")
}

// Run 启动 HTTP 服务
func (s *Server) Run(port string) error {
	log.Printf("[INFO][api] Run HTTP 启动监听 port=%s", port)
	return s.router.Run(":" + port)
}
