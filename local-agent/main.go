package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	clientVersion = "0.1.2"
	localCtrlAddr = "127.0.0.1:17000" // 本地控制服务地址（前端登录后激活用，仅本机可访问）
)

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
	flagServer := flag.String("server", "ws://127.0.0.1:8001", "中央服务器 WebSocket 地址（如 ws://127.0.0.1:8001）")
	flagToken := flag.String("token", "", "JWT access token（可选；也可用 AGENT_TOKEN 环境变量，或由前端登录后自动激活）")
	flagWorkDir := flag.String("workdir", "", "agent 文件操作工作目录（默认 ./workspace；可用 AGENT_WORK_DIR 环境变量覆盖）")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("local-agent %s (go runtime: %s/%s)\n", clientVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	serverURL = *flagServer
	if *flagToken == "" {
		*flagToken = os.Getenv("AGENT_TOKEN")
	}
	setToken(*flagToken)

	// 初始化 agent 工作目录（沙箱根，与 local-agent 目录分离）
	if *flagWorkDir == "" {
		*flagWorkDir = os.Getenv("AGENT_WORK_DIR")
	}
	initWorkDir(*flagWorkDir)

	log.Printf("[INFO] local-agent 启动 version=%s platform=%s", clientVersion, runtime.GOOS)
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

// withCORS 允许前端(不同端口)跨域调用本地控制服务
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
