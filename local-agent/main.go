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
	"time"

	"github.com/gorilla/websocket"
)

const clientVersion = "0.1.1"

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
	serverURL := flag.String("server", "ws://127.0.0.1:8001", "中央服务器 WebSocket 地址（如 ws://127.0.0.1:8001）")
	token := flag.String("token", "", "JWT access token（鉴权用，也可用 AGENT_TOKEN 环境变量）")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("local-agent %s (go runtime: %s/%s)\n", clientVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	if *token == "" {
		*token = os.Getenv("AGENT_TOKEN")
	}
	if *token == "" {
		log.Fatalf("[FATAL] 缺少 token，请用 -token 参数或 AGENT_TOKEN 环境变量指定")
	}

	wsURL := *serverURL + "/ws/agent?token=" + *token
	log.Printf("[INFO] local-agent 启动 version=%s platform=%s", clientVersion, runtime.GOOS)
	log.Printf("[INFO] 目标服务器: %s", *serverURL)

	// 断线重连（指数退避，最大间隔 30s）
	backoff := time.Second
	maxBackoff := 30 * time.Second
	for {
		err := runSession(wsURL)
		if err != nil {
			log.Printf("[WARN] 会话结束: %v", err)
		}
		log.Printf("[INFO] %v 后重连...", backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runSession 建立一次 WebSocket 会话，直到断开
func runSession(wsURL string) error {
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
	defer conn.Close()
	log.Printf("[INFO] 连接建立成功")

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
