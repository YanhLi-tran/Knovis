package api

import (
	"io"
	"log"
	"net/http"

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

	"github.com/zeromicro/go-zero/rest"
)

// Server HTTP API 服务器（go-zero rest 实现）
type Server struct {
	server          *rest.Server
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
	auditLogger *audit.Logger  // P3: 审计日志（异步写）
	wsHandler   *ws.Handler    // P4: WebSocket 升级处理器（本地客户端连接，file/sandbox 工具通道）
	docClient   *rag.DocClient // P5: doc-service 客户端(RAG 文档管理 + 检索)
	knovisClient *knovis.Client // Knovis 客户端（/auth/me 透传用户资料）
}

// NewServer 创建 API 服务器
func NewServer(orch *orchestrator.Orchestrator, qMgr *tools.QuestionManager, cfgMgr *config.Manager, repos *storage.Repositories, memSvc *memory.Service, ttlSch *memory.TTLScheduler, msgTtlSch *memory.MessageTTLScheduler, authSvc *auth.AuthService, authMode string, wsHub *ws.Hub, approvalMgr *tools.ApprovalManager, docClient *rag.DocClient, knovisClient *knovis.Client) *Server {
	s := &Server{
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
	return s
}

// Start 启动 go-zero rest 服务
func (s *Server) Start(host string, port int) error {
	srv := rest.MustNewServer(rest.RestConf{
		Host: host,
		Port: port,
	}, rest.WithFileServer("/static", http.Dir("./static")))
	s.server = srv
	defer srv.Stop()

	// 中间件：CORS + 鉴权（P3: strict 仅 JWT / dev JWT 优先 + X-Client-ID 降级）
	srv.Use(s.corsMiddleware)
	srv.Use(s.AuthMiddleware())

	s.registerRoutes(srv)
	log.Printf("[INFO][api] go-zero rest 服务创建成功 authMode=%s", s.authMode)
	srv.Start()
	return nil
}

// corsMiddleware CORS 中间件（阶段一允许所有源）
func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-LLM-API-Key, X-Client-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// registerRoutes 注册路由（go-zero rest）
func (s *Server) registerRoutes(srv *rest.Server) {
	// 健康检查 + 工具列表
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/health", Handler: s.adapt(s.health)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/tools", Handler: s.adapt(s.listTools)})
	// local-agent 下载引导信息（公开，前端引导弹窗用）
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/agent/local/info", Handler: s.adapt(s.getLocalAgentInfo)})

	// SSO 形态：注册/登录/刷新由 Knovis 提供，agent-go 仅保留 logout（公开）+ me + 凭证管理（需鉴权）
	// 反向代理 Knovis 登录/注册接口(避免前端跨端口调用被浏览器拦截)
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/auth/login", Handler: s.proxyKnovisEndpoint("/login")})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/auth/register", Handler: s.proxyKnovisEndpoint("/register")})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/auth/logout", Handler: s.adapt(s.logout)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/auth/me", Handler: s.adapt(s.me)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/auth/llm-key", Handler: s.adapt(s.setLLMKey)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/auth/llm-key", Handler: s.adapt(s.getLLMKey)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/auth/llm-key", Handler: s.adapt(s.deleteLLMKey)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/auth/knovis-token", Handler: s.adapt(s.setKnovisToken)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/auth/knovis-token", Handler: s.adapt(s.getKnovisToken)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/auth/knovis-token", Handler: s.adapt(s.deleteKnovisToken)})

	// P4: WebSocket 端点（本地客户端连接，file/sandbox 工具通过此通道下发指令）
	// 鉴权由 ws.Handler 自行处理（query token），中间件白名单已放行 /ws/*
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/ws/agent", Handler: s.wsHandler.HandleWS})

	// Session 管理
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/sessions", Handler: s.adapt(s.createSession)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/sessions", Handler: s.adapt(s.listSessions)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/sessions/:id", Handler: s.adapt(s.getSession)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/sessions/:id/messages", Handler: s.adapt(s.listSessionMessages)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/sessions/:id/messages/summarized", Handler: s.adapt(s.listSummarizedMessages)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/sessions/:id/messages/:mid/restore", Handler: s.adapt(s.restoreMessage)})
	srv.AddRoute(rest.Route{Method: http.MethodPatch, Path: "/sessions/:id", Handler: s.adapt(s.updateSession)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/sessions/:id", Handler: s.adapt(s.deleteSession)})

	// P2: 项目管理 + 记忆系统
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/projects", Handler: s.adapt(s.createProject)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/projects", Handler: s.adapt(s.listProjects)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/projects/:id", Handler: s.adapt(s.getProject)})
	srv.AddRoute(rest.Route{Method: http.MethodPatch, Path: "/projects/:id", Handler: s.adapt(s.updateProject)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/projects/:id", Handler: s.adapt(s.deleteProject)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/projects/:id/sessions", Handler: s.adapt(s.listProjectSessions)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/projects/:id/memories", Handler: s.adapt(s.createMemory)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/projects/:id/memories", Handler: s.adapt(s.listMemories)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/projects/:id/memories/embed", Handler: s.adapt(s.embedPending)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/projects/:id/memories/search", Handler: s.adapt(s.searchMemories)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/projects/:id/memories/archived", Handler: s.adapt(s.listArchivedMemories)})

	// 单条记忆操作（跨项目通用）
	srv.AddRoute(rest.Route{Method: http.MethodPut, Path: "/memory/memories/:id", Handler: s.adapt(s.updateMemory)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/memory/memories/:id", Handler: s.adapt(s.deleteMemory)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/memory/memories/:id/archive", Handler: s.adapt(s.archiveMemory)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/memory/archive/:id/restore", Handler: s.adapt(s.restoreMemory)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/memory/ttl/run", Handler: s.adapt(s.triggerTTL)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/memory/message-ttl/run", Handler: s.adapt(s.triggerMessageTTL)})

	// 用户档案
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/memory/user-config", Handler: s.adapt(s.getUserConfig)})
	srv.AddRoute(rest.Route{Method: http.MethodPut, Path: "/memory/user-config", Handler: s.adapt(s.upsertUserConfig)})

	// 对话流（SSE）— 手动注册原始 http.HandlerFunc，保持 SSE 事件循环逻辑不变
	// 注：go-zero rest 的 responseWriter 实现了 http.Flusher，SSE 可正常 flush
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/chat/stream", Handler: s.adapt(s.chatStream)})

	// 用户回答提问
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/question/:question_id/answer", Handler: s.adapt(s.answerQuestion)})

	// P4: 工具执行审批（file_write/sandbox_exec 等需审批工具，前端收到 waiting_approval SSE 后调此接口）
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/approval/:approval_id/decide", Handler: s.adapt(s.decideApproval)})

	// P5: RAG 文档管理 + 检索调试
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/documents/upload", Handler: s.adapt(s.uploadDocument)})
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/documents", Handler: s.adapt(s.listDocuments)})
	srv.AddRoute(rest.Route{Method: http.MethodDelete, Path: "/documents/:id", Handler: s.adapt(s.deleteDocument)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/documents/scan", Handler: s.adapt(s.scanDocuments)})
	srv.AddRoute(rest.Route{Method: http.MethodPost, Path: "/rag/debug", Handler: s.adapt(s.ragDebug)})

	// 静态文件服务（前端页面）— 用 rest.WithFileServer 注册（见 Start）
	// 根路径返回 index.html
	srv.AddRoute(rest.Route{Method: http.MethodGet, Path: "/", Handler: func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./static/index.html")
	}})

	log.Printf("[INFO][api] registerRoutes 路由注册完成")
}

// adapt 将 GinCompat 风格 handler 适配为 http.HandlerFunc
func (s *Server) adapt(h func(*GinCompat)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(NewGinCompat(w, r))
	}
}

// proxyKnovisEndpoint 反向代理 Knovis 公开接口（登录/注册等，同源调用避免跨端口被浏览器拦截）
// path 为 Knovis 侧路径（如 /login、/register），Knovis 地址统一来自 KNOVIS_API_BASE_URL 配置
func (s *Server) proxyKnovisEndpoint(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.knovisClient == nil {
			http.Error(w, "Knovis 客户端未配置", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "读取请求体失败", http.StatusBadRequest)
			return
		}
		r.Body.Close()

		code, out, err := s.knovisClient.PostPublic(r.Context(), path, body)
		if err != nil {
			log.Printf("[WARN][api] Knovis 反代失败 path=%s err=%v", path, err)
			http.Error(w, "Knovis 服务不可达", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write(out)
	}
}
