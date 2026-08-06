package config

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Soul 全局人格设定
type Soul struct {
	Identity struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		Creator string `yaml:"creator"`
	} `yaml:"identity"`
	Personality struct {
		Tone   string   `yaml:"tone"`
		Style  string   `yaml:"style"`
		Values []string `yaml:"values"`
	} `yaml:"personality"`
	Greeting     string `yaml:"greeting"`
	ThinkingMode string `yaml:"thinking_mode"`
}

// Rule 单条规则
type Rule struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Enabled     bool   `yaml:"enabled"`
}

// Limits 全局限额
type Limits struct {
	MaxOTACOIterations   int `yaml:"max_otaco_iterations"`
	MaxConsecutiveErrors int `yaml:"max_consecutive_errors"`
	MaxRetryPerTool      int `yaml:"max_retry_per_tool"`
	LLMTimeoutSeconds    int `yaml:"llm_timeout_seconds"`
	ToolTimeoutSeconds   int `yaml:"tool_timeout_seconds"`
}

// RuleBasic 全局固定规则
type RuleBasic struct {
	Rules  []Rule `yaml:"rules"`
	Limits Limits `yaml:"limits"`
}

// AppConfig 应用配置（环境变量）
type AppConfig struct {
	Port       string
	Env        string
	LLMAPIKey  string
	LLMBaseURL string
	LLMModel   string
	LLMChatPath string
	// P2: 记忆系统
	MemoryServiceURL      string // Python 子服务地址（embedding + 混合检索）
	MemoryEmbedBatchRounds int    // 累计多少轮后批量 embed
	MemoryInjectProject   bool   // 是否注入项目记忆 RAG top-5
	MemoryInjectSoul      bool   // soul 是否默认注入
	// P3: 鉴权配置（SSO 形态：只校验 Knovis 签发的 JWT，不自管签发）
	AuthMode  string // strict（仅 JWT）/ dev（JWT 优先 + X-Client-ID 降级，过渡期默认）
	JWTSecret string // HS256 密钥（必须与 Knovis 的 JWT_SECRET 一致）
	JWTIssuer string // 签发者（与 Knovis 的 JWT_ISSUER 一致，如 Knovis）
	JWTAudience string // 受众（与 Knovis 的 JWT_AUDIENCE 一致，如 agent-go）
	// P4: Knovis 用户/动态数据 API（Skill 按需加载，用户自带 token）
	KnovisAPIBaseURL string // Knovis API 基础地址
	// P5: RAG 文档服务(doc-service,8003)
	DocServiceURL string // doc-service 地址(PDF 摄入 + RAG 检索)
	// P6: local-agent 分发（前端下载引导用）
	AgentDownloadBaseURL string // local-agent 下载引导地址(默认本项目 GitHub Releases)
}

// Manager 配置管理器（含热加载）
type Manager struct {
	mu       sync.RWMutex
	soul     *Soul
	ruleBasic *RuleBasic
	appCfg   *AppConfig
	configDir string
	stopCh   chan struct{}
}

var (
	instance *Manager
	once     sync.Once
)

// GetManager 获取单例配置管理器
func GetManager() *Manager {
	once.Do(func() {
		instance = &Manager{
			appCfg:  loadAppConfig(),
			stopCh:  make(chan struct{}),
		}
	})
	return instance
}

// Init 初始化加载 YAML 配置并启动热加载
func (m *Manager) Init(configDir string) error {
	m.configDir = configDir
	if err := m.reload(); err != nil {
		return err
	}
	go m.hotReloadLoop()
	log.Println("[config] 配置加载完成，热加载已启动，目录:", configDir)
	return nil
}

// reload 重新加载 YAML 配置（保留旧版本如果加载失败）
func (m *Manager) reload() error {
	soulPath := filepath.Join(m.configDir, "soul.yaml")
	rulePath := filepath.Join(m.configDir, "agent_rule_basic.yaml")

	newSoul, err := loadYAML[Soul](soulPath)
	if err != nil {
		log.Printf("[config] soul.yaml 加载失败，保留旧版本: %v", err)
	} else {
		m.mu.Lock()
		m.soul = newSoul
		m.mu.Unlock()
	}

	newRule, err := loadYAML[RuleBasic](rulePath)
	if err != nil {
		log.Printf("[config] agent_rule_basic.yaml 加载失败，保留旧版本: %v", err)
	} else {
		m.mu.Lock()
		m.ruleBasic = newRule
		m.mu.Unlock()
	}

	return nil
}

// hotReloadLoop 轮询检测文件变化（每 5 秒）
func (m *Manager) hotReloadLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	modTimes := map[string]time.Time{}

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			changed := false
			for _, name := range []string{"soul.yaml", "agent_rule_basic.yaml"} {
				p := filepath.Join(m.configDir, name)
				info, err := os.Stat(p)
				if err != nil {
					continue
				}
				if prev, ok := modTimes[name]; !ok || !info.ModTime().Equal(prev) {
					modTimes[name] = info.ModTime()
					changed = true
				}
			}
			if changed {
				log.Println("[config] 检测到配置文件变化，重新加载...")
				m.reload()
			}
		}
	}
}

// GetSoul 获取 Soul（线程安全）
func (m *Manager) GetSoul() *Soul {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.soul
}

// GetRuleBasic 获取全局规则（线程安全）
func (m *Manager) GetRuleBasic() *RuleBasic {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ruleBasic
}

// GetAppConfig 获取应用配置
func (m *Manager) GetAppConfig() *AppConfig {
	return m.appCfg
}

// loadYAML 泛型加载 YAML 文件
func loadYAML[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result T
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// loadAppConfig 从环境变量加载应用配置
func loadAppConfig() *AppConfig {
	return &AppConfig{
		Port:       getEnv("AGENT_PORT", "8001"),
		Env:        getEnv("AGENT_ENV", "development"),
		LLMAPIKey:  getEnv("LLM_API_KEY", ""),
		LLMBaseURL: getEnv("LLM_BASE_URL", "https://api.deepseek.com"),
		LLMModel:   getEnv("LLM_MODEL", "deepseek-v4-flash"),
		LLMChatPath: getEnv("LLM_CHAT_PATH", "/chat/completions"),
		// P2: 记忆系统
		MemoryServiceURL:       getEnv("MEMORY_SERVICE_URL", "http://127.0.0.1:8002"),
		MemoryEmbedBatchRounds: getEnvInt("MEMORY_EMBED_BATCH_ROUNDS", 5),
		MemoryInjectProject:    getEnvBool("MEMORY_INJECT_PROJECT", true),
		MemoryInjectSoul:       getEnvBool("MEMORY_INJECT_SOUL", true),
		// P3: 鉴权（SSO 形态：只校验 Knovis 签发的 JWT；dev 模式 JWT 优先 + X-Client-ID 降级，过渡期兼容老前端）
		AuthMode:    getEnv("AUTH_MODE", "dev"),
		JWTSecret:   getEnv("JWT_SECRET", ""),
		JWTIssuer:   getEnv("JWT_ISSUER", "Knovis"),
		JWTAudience: getEnv("JWT_AUDIENCE", "agent-go"),
		// P4: Knovis 用户/动态数据 API
		KnovisAPIBaseURL: getEnv("KNOVIS_API_BASE_URL", "http://127.0.0.1:8080"),
		// P5: RAG 文档服务
		DocServiceURL: getEnv("DOC_SERVICE_URL", "http://127.0.0.1:8003"),
		// local-agent 下载引导地址（可配置：自建下载服务器/内网部署时覆盖）
		AgentDownloadBaseURL: getEnv("AGENT_DOWNLOAD_BASE_URL", "https://github.com/YanhLi-tran/Knovis/releases/latest"),
	}
}

// getEnvInt 从环境变量读取 int（失败用 fallback）
func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// getEnvBool 从环境变量读取 bool（true/1/yes 视为真）
func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
