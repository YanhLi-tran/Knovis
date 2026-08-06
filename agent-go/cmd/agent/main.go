package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-go/internal/api"
	"agent-go/internal/auth"
	"agent-go/internal/config"
	"agent-go/internal/crypto"
	"agent-go/internal/knovis"
	"agent-go/internal/memory"
	"agent-go/internal/orchestrator"
	"agent-go/internal/rag"
	"agent-go/internal/storage"
	"agent-go/internal/tools"
	"agent-go/internal/tools/file"
	"agent-go/internal/tools/info"
	"agent-go/internal/tools/sandbox"
	"agent-go/internal/tools/skill"
	"agent-go/internal/tools/skill/skills"
	"agent-go/internal/ws"

	"github.com/zeromicro/go-zero/core/conf"

	"gorm.io/gorm"
)

var configFile = flag.String("f", "etc/agent-api.yaml", "the config file")

func main() {
	flag.Parse()

	// 日志格式: 带时间戳 + 文件名 + 行号,便于排障
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 加载 .env（环境变量优先级：环境变量 > yaml > 默认值）
	loadEnv(".env")

	// 加载 go-zero 配置（etc/agent-api.yaml，支持 ${VAR:-default} 展开）
	var c config.RestConfig
	conf.MustLoad(*configFile, &c)

	// 初始化配置管理器（soul.yaml / agent_rule_basic.yaml 热加载）
	configDir := filepath.Join(".", "configs")
	cfgMgr := config.GetManager()
	if err := cfgMgr.Init(configDir); err != nil {
		log.Fatalf("配置初始化失败: %v", err)
	}
	appCfg := cfgMgr.GetAppConfig()

	// P4: WebSocket hub（本地客户端连接管理，file/sandbox 工具通过 hub 下发指令到用户本地）
	// 早于工具注册创建：file/sandbox 工具注册时需注入 hub
	wsHub := ws.NewHub()

	// P4: 审批管理器（写操作/危险工具审批流，复用 QuestionManager 模式）
	// 早于编排器创建：NewOrchestrator 需注入 approvalMgr
	approvalMgr := tools.NewApprovalManager()

	// P4: Skill 注册表 + 管理器（Layer 2/3：低频复杂工具按需加载）
	// 注册表存全局 Skill 元信息（注入 system prompt，每个约 25 tokens）
	// 管理器存 session 级已加载状态（load_skill 后常驻到对话结束）
	// 注意：knovis Skill 注册延后到 authSvc 初始化之后（依赖 authSvc 解密用户 token）
	skillReg := skill.NewRegistry()
	skillMgr := skill.NewManager(skillReg)

	// 创建工具注册表 + 注册工具
	// Layer 1（FC 常驻）：info（天气/搜索/RAG）+ file（read/write/grep）+ sandbox（exec）
	// Layer 2/3（Skill 按需加载）：load_skill 常驻 FC，knovis 等通过 load_skill 拉取
	// P5: doc-service 客户端(RAG 文档检索 + 文档管理),早于工具注册创建
	// P4: Knovis 客户端(用户/动态数据 owner,/auth/me 透传 + knovis Skill 复用)
	docClient := rag.NewDocClient(appCfg.DocServiceURL)
	knovisClient := knovis.NewClient(appCfg.KnovisAPIBaseURL)
	registry := tools.NewRegistry()
	info.RegisterWeatherTools(registry)
	info.RegisterWebSearchTools(registry)
	info.RegisterRAGSearchTools(registry, docClient) // P5: RAG 文档检索(FC 常驻,与 web_search 同级)
	file.RegisterFileTools(registry, wsHub)       // P4: 文件操作（read 免审批/write 需审批）
	sandbox.RegisterSandboxTools(registry, wsHub) // P4: 沙箱命令（全部需审批）
	log.Printf("[main] 已注册 %d 个工具", len(registry.List()))

	// 初始化 MySQL 存储（重试 3 次，间隔 2s）
	var repos *storage.Repositories
	{
		dbCfg := storage.LoadDBConfig()
		var gormDB *gorm.DB
		for attempt := 1; attempt <= 3; attempt++ {
			d, err := storage.InitDB(dbCfg)
			if err == nil {
				gormDB = d
				break
			}
			log.Printf("[main] MySQL 连接失败(%d/3): %v", attempt, err)
			if attempt < 3 {
				time.Sleep(2 * time.Second)
			}
		}
		if gormDB == nil {
			log.Fatalf("[main] MySQL 连接重试 3 次仍失败，退出")
		}
		repos = storage.NewRepositories(gormDB).WithCache(storage.NewCache(storage.LoadRedisConfig()))
	}

	// 创建提问管理器
	questionMgr := tools.NewQuestionManager()

	// P2: 创建记忆服务（HTTP 调 Python 子服务 + Redis 缓存 + MySQL 仓储）
	memClient := memory.NewMemoryClient(cfgMgr.GetAppConfig())
	memSvc := memory.NewService(cfgMgr, repos, memClient)

	// P2: 启动 TTL 定时任务（每周一 03:00 归档超期记忆 + 清理过期归档）
	ttlScheduler := memory.NewTTLScheduler(memSvc)
	ttlScheduler.Start()
	defer ttlScheduler.Stop()

	// P2: 启动消息 TTL 定时任务（每周一 04:00 软删超期未恢复的已压缩消息）
	// 与记忆 TTL 错开 1 小时，避免同时执行造成峰值
	msgTtlScheduler := memory.NewMessageTTLScheduler(repos)
	msgTtlScheduler.Start()
	defer msgTtlScheduler.Stop()

	// 创建 OTACO 编排器（P4: 注入 approvalMgr + skillMgr + skillReg）
	// - approvalMgr: 需审批工具在 processToolCalls 拦截走审批流
	// - skillMgr: load_skill 按需加载 + session 级工具执行
	// - skillReg: Skill 元信息注入 system prompt（工具列表后、记忆块前）
	orch := orchestrator.NewOrchestrator(registry, cfgMgr, questionMgr, repos, memSvc, approvalMgr, skillMgr, skillReg)

	// P3: 初始化鉴权服务（SSO 形态：只校验 Knovis 签发的 JWT，不自管签发）
	// 注册/登录/刷新由用户在 Knovis 侧完成，agent-go 不提供
	authMode := appCfg.AuthMode

	// P3: 初始化主密钥管理器（AES-256-GCM 加密用户 LLM key / Knovis token）
	// dev 模式允许未配置主密钥时使用随机兜底密钥；strict 模式必须配置 MASTER_KEY_V1
	masterKey, mkErr := crypto.NewMasterKeyManager(authMode != "strict")
	if mkErr != nil {
		log.Fatalf("[main] 主密钥初始化失败: %v", mkErr)
	}

	var authSvc *auth.AuthService
	if appCfg.JWTSecret == "" {
		if authMode == "strict" {
			log.Fatalf("[main] strict 模式必须配置 JWT_SECRET（与 Knovis 一致）")
		}
		// dev 模式无 secret：鉴权服务不可用，中间件降级到 X-Client-ID
		log.Printf("[WARN][main] JWT_SECRET 未配置，dev 模式降级为仅 X-Client-ID 鉴权（仅供开发测试）")
	} else {
		jwtCfg := &auth.JWTConfig{
			Secret:   appCfg.JWTSecret,
			Issuer:   appCfg.JWTIssuer,
			Audience: appCfg.JWTAudience,
		}
		authSvc = auth.NewAuthService(repos.User, repos.Cache, jwtCfg, masterKey)
		log.Printf("[main] 鉴权服务已启用 authMode=%s issuer=%s", authMode, appCfg.JWTIssuer)
	}

	// P4: 注册 knovis Skill（低频复杂 → Skill 按需加载，只读）
	// authSvc 为 nil 时（dev 模式无 JWT secret）仍可注册：load_skill 时 resolveToken 会返回明确错误
	skillReg.Register(skills.NewKnovisSkillDefinition(authSvc, knovisClient))
	// P6: 注册 kb_summary Skill（企业知识库总结，按需加载；依赖 doc-service，无需用户 token）
	skillReg.Register(skills.NewKBSummarySkillDefinition(docClient))

	// 创建 API 服务器（go-zero rest）并启动
	server := api.NewServer(orch, questionMgr, cfgMgr, repos, memSvc, ttlScheduler, msgTtlScheduler, authSvc, authMode, wsHub, approvalMgr, docClient, knovisClient)

	port := c.Port
	if port == 0 {
		port = 8001
	}
	log.Printf("[main] Agent Go Service 启动在 http://%s:%d", c.Host, port)
	log.Printf("[main] 接口: POST /chat/stream | GET /health | GET /tools | POST /question/:id/answer | POST /auth/logout | GET /auth/me | GET /ws/agent")

	if err := server.Start(c.Host, port); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// loadEnv 简单的 .env 文件加载器
func loadEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		// .env 不存在不报错，用系统环境变量
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 去除可能的引号
		line = strings.Trim(line, "\"")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"'")
		// 不覆盖已存在的环境变量
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
