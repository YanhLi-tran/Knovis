package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	localCtrlAddr = "127.0.0.1:17000" // 本地控制服务地址（前端登录后激活用，仅本机可访问）
	configFile    = "config.json"     // 本地配置文件名（存服务器地址等，双击运行时读取）
)

// clientVersion local-agent 版本号
// 发布时可用 -ldflags "-X main.clientVersion=vX.Y.Z" 注入（GitHub Actions 按 tag 自动注入）
var clientVersion = "0.1.3"

// 全局状态（本地控制服务与 WS 连接循环共享）
// token 采用「登录时由前端推送激活」模式：local-agent 常驻，userID 始终跟随当前登录用户
var (
	tokenMu sync.RWMutex
	curToken string
	connMu   sync.Mutex
	curConn  *websocket.Conn
	connected atomic.Bool
	serverURL string
)

// localConfig 本地配置（首次双击运行时引导用户填写，保存到 exe 旁的 config.json）
// 解决"双击运行用默认 127.0.0.1 连不上"问题：用户填一次服务器地址，后续双击自动读取
type localConfig struct {
	Server  string `json:"server"`  // 中央服务器 WebSocket 地址（如 ws://219.146.211.42:20169）
	WorkDir string `json:"workdir"` // agent 工作目录（空=用 exe 旁的 ./workspace）
}

// configPath 返回配置文件绝对路径（exe 旁的 config.json）
func configPath() string {
	exe, err := os.Executable()
	if err != nil {
		return configFile
	}
	return filepath.Join(filepath.Dir(exe), configFile)
}

// loadConfig 读取本地配置，不存在返回零值
func loadConfig() localConfig {
	var cfg localConfig
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

// saveConfig 写入本地配置（双击运行首次配置后保存，后续启动免填）
func saveConfig(cfg localConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o644)
}

// promptConfig 双击运行（无命令行参数）且无配置文件时，交互式引导用户填写服务器地址
// 填完后保存到 config.json，下次双击直接用，不再询问
func promptConfig() localConfig {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("============================================")
	fmt.Println("  local-agent 首次配置")
	fmt.Println("============================================")
	fmt.Println()
	fmt.Println("请输入 Knovis 服务器地址（agent-go 的 WebSocket 地址）")
	fmt.Println("格式: ws://<公网IP>:<端口>  例如: ws://219.146.211.42:20169")
	fmt.Println("（端口在并行智算云控制台的端口映射里查，是 8001 映射后的公网端口）")
	fmt.Print("> ")
	server, _ := reader.ReadString('\n')
	server = strings.TrimSpace(server)

	cfg := localConfig{Server: server}
	if err := saveConfig(cfg); err != nil {
		log.Printf("[WARN] 配置保存失败(下次仍需输入): %v", err)
	} else {
		log.Printf("[INFO] 配置已保存到 %s，下次双击运行自动读取", configPath())
	}
	fmt.Println()
	return cfg
}

func setToken(t string) {
	tokenMu.Lock()
	curToken = t
	tokenMu.Unlock()
}

func getToken() string {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	return curToken
}

// kickConn 主动断开当前 WS 连接（token 更换时触发重连循环用新 token 重连）
func kickConn() {
	connMu.Lock()
	if curConn != nil {
		_ = curConn.Close()
	}
	connMu.Unlock()
}

// session 单次 WebSocket 会话
// 写并发安全：gorilla/websocket 的 WriteMessage 非线程安全，多 goroutine 写需加锁
// （心跳 goroutine + 多个 handleCommand goroutine 会并发写）
type session struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// writeJSON 线程安全地写 JSON 消息
func (s *session) writeJSON(msg clientMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func main() {
	flagServer := flag.String("server", "", "中央服务器 WebSocket 地址（如 ws://219.146.211.42:20169）。留空则读 config.json，都没有则交互式输入")
	flagToken := flag.String("token", "", "JWT access token（可选；也可用 AGENT_TOKEN 环境变量，或由前端登录后自动激活）")
	flagWorkDir := flag.String("workdir", "", "agent 文件操作工作目录（默认 ./workspace；可用 AGENT_WORK_DIR 环境变量覆盖）")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("local-agent %s (go runtime: %s/%s)\n", clientVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	// 服务器地址解析优先级: 命令行 -server > 环境变量 AGENT_SERVER > config.json > 交互式输入(首次双击)
	cfg := loadConfig()
	serverURL = *flagServer
	if serverURL == "" {
		serverURL = os.Getenv("AGENT_SERVER")
	}
	if serverURL == "" {
		serverURL = cfg.Server
	}
	if serverURL == "" {
		// 双击运行且无配置：交互式引导填写服务器地址，保存到 config.json
		cfg = promptConfig()
		serverURL = cfg.Server
	}

	if *flagToken == "" {
		*flagToken = os.Getenv("AGENT_TOKEN")
	}
	setToken(*flagToken)

	// 工作目录解析优先级: 命令行 -workdir > 环境变量 AGENT_WORK_DIR > config.json > 默认 ./workspace
	if *flagWorkDir == "" {
		*flagWorkDir = os.Getenv("AGENT_WORK_DIR")
	}
	if *flagWorkDir == "" {
		*flagWorkDir = cfg.WorkDir
	}
	initWorkDir(*flagWorkDir)

	log.Printf("[INFO] local-agent 启动 version=%s platform=%s", clientVersion, runtime.GOOS)
	log.Printf("[INFO] 服务器: %s", serverURL)
	log.Printf("[INFO] 本地控制服务: http://%s（前端登录后自动激活，绑定当前用户）", localCtrlAddr)

	// 本地控制 HTTP 服务（goroutine，供前端登录/注册成功后激活 token）
	go startLocalCtrl()

	// 断线重连（指数退避，最大间隔 30s）
	backoff := time.Second
	for {
		if getToken() == "" {
			log.Printf("[INFO] 等待 token 激活（可用 -token 参数 / AGENT_TOKEN 环境变量，或在前端登录后自动激活）...")
			time.Sleep(2 * time.Second)
			continue
		}
		err := runSession()
		if err != nil {
			log.Printf("[WARN] 会话结束: %v", err)
		}
		// token 被更换时立即重连（重置退避），否则指数退避
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		log.Printf("[INFO] %v 后重连...", backoff)
		time.Sleep(backoff)
	}
}

// runSession 建立一次 WebSocket 会话，直到断开
func runSession() error {
	token := getToken()
	if token == "" {
		return fmt.Errorf("无 token")
	}
	wsURL := serverURL + "/ws/agent?token=" + token + "&role=agent"
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, resp, err := dialer.Dial(wsURL, http.Header{})
	if err != nil {
		if resp != nil {
			log.Printf("[ERROR] 连接失败 status=%d err=%v", resp.StatusCode, err)
		} else {
			log.Printf("[ERROR] 连接失败 err=%v", err)
		}
		return err
	}
	// 连接期间 token 可能已被前端更换：用旧 token 建立的连接立即关闭，交给重连循环
	if getToken() != token {
		_ = conn.Close()
		return fmt.Errorf("token 已变更，重连")
	}

	connMu.Lock()
	curConn = conn
	connMu.Unlock()
	defer func() {
		connMu.Lock()
		curConn = nil
		connMu.Unlock()
	}()
	defer conn.Close()

	log.Printf("[INFO] 连接建立成功")
	connected.Store(true)
	defer connected.Store(false)

	s := &session{conn: conn}

	// 发送 register（声明客户端能力）
	regMsg := clientMessage{
		Type:          msgTypeRegister,
		ClientVersion: clientVersion,
		Platform:      runtime.GOOS,
		Capabilities:  []string{cmdFileRead, cmdFileWrite, cmdGrep, cmdSandboxExec, cmdFileList},
	}
	if err := s.writeJSON(regMsg); err != nil {
		return err
	}

	// 心跳 goroutine（25s 发一次文本心跳，服务端 ping/pong 探活为主）
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.writeJSON(clientMessage{Type: msgTypeHeartbeat})
			case <-done:
				return
			}
		}
	}()

	// 读循环：收 command，异步分发执行
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var cmd serverCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			log.Printf("[WARN] 消息解析失败: %v", err)
			continue
		}
		if cmd.Type != "command" {
			continue // 忽略非 command 消息（ping/pong 由 gorilla 底层处理）
		}
		if cmd.TraceID != "" {
			log.Printf("[INFO] 收到命令 type=%s request_id=%s trace_id=%s", cmd.CommandType, cmd.RequestID, cmd.TraceID)
		}
		// 异步执行（支持多指令并发，客户端按自身能力并行处理）
		go handleCommand(s, cmd)
	}
}

// sendResult 回传执行结果
func (s *session) sendResult(requestID, status, result, errMsg string) {
	msg := clientMessage{
		Type:      msgTypeResult,
		RequestID: requestID,
		Status:    status,
		Result:    result,
		Error:     errMsg,
	}
	if err := s.writeJSON(msg); err != nil {
		log.Printf("[ERROR] 回传结果失败: %v", err)
	}
}

// ==================== 本地控制服务（前端激活用）====================

// startLocalCtrl 启动本地 HTTP 控制服务
// 端点：
//   - POST /activate  body {"token":"<JWT>"}  用新 token 激活并重连（绑定当前登录用户）
//   - GET  /status                            返回运行/连接状态
// 仅监听 127.0.0.1，不对外暴露；带 CORS 头允许前端(8001)跨域调用。
func startLocalCtrl() {
	mux := http.NewServeMux()
	mux.HandleFunc("/activate", withCORS(handleActivate))
	mux.HandleFunc("/status", withCORS(handleStatus))

	srv := &http.Server{Addr: localCtrlAddr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[ERROR] 本地控制服务退出: %v", err)
	}
}

// withCORS 允许前端(公网网页)跨域调用本地控制服务(127.0.0.1:17000)
// 关键: Chrome/Edge 的 Private Network Access 规则要求本地服务在 OPTIONS 预检响应里
// 显式返回 Access-Control-Allow-Private-Network: true, 否则公网页面 fetch 127.0.0.1 会被拦截,
// 导致前端登录后无法把 token 推给 local-agent(local-agent 一直卡在"等待 token 激活")
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		// Private Network Access: 允许公网页面访问本机服务(127.0.0.1)
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// handleActivate 前端登录/注册成功后推送当前用户 token
func handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	if body.Token == "" {
		http.Error(w, "缺少 token", http.StatusBadRequest)
		return
	}
	setToken(body.Token)
	kickConn() // 断开旧连接，重连循环用新 token 重连（userID 与当前登录用户一致）
	log.Printf("[INFO] 已激活新 token，正在重连 agent-go...")
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"status":"ok"}`)
}

// handleStatus 返回本地 agent 运行/连接状态（前端展示用）
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"running":true,"connected":%v,"version":%q}`, connected.Load(), clientVersion)
}
