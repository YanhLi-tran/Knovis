package main

// WebSocket 消息协议（与服务端 agent-go/internal/ws/message.go 保持一致）
// 客户端收 serverCommand（服务端→客户端 command），发 clientMessage（客户端→服务端）。
const (
	msgTypeRegister  = "register"  // 客户端→服务端：声明能力（升级后首条）
	msgTypeHeartbeat = "heartbeat" // 客户端→服务端：心跳保活
	msgTypeResult    = "result"    // 客户端→服务端：回传执行结果
	msgTypeError     = "error"     // 客户端→服务端：错误报告
)

// 指令类型（与服务端 ws.Cmd* 一一对应）
const (
	cmdFileRead    = "file_read"
	cmdFileWrite   = "file_write"
	cmdGrep        = "grep"
	cmdSandboxExec = "sandbox_exec"
	cmdFileList    = "file_list" // P2 新增：列目录内容
)

const (
	statusSuccess = "success"
	statusError   = "error"
)

// serverCommand 服务端 → 客户端指令
type serverCommand struct {
	Type        string         `json:"type"`         // command
	RequestID   string         `json:"request_id"`   // 指令追踪 ID
	CommandType string         `json:"command_type"` // file_read/file_write/grep/sandbox_exec
	Args        map[string]any `json:"args"`         // 指令参数
	Timeout     int            `json:"timeout"`      // 超时秒数
}

// clientMessage 客户端 → 服务端消息（register/result/heartbeat/error 统一信封）
type clientMessage struct {
	Type          string   `json:"type"`
	RequestID     string   `json:"request_id,omitempty"`
	Status        string   `json:"status,omitempty"`        // result: success/error
	Result        string   `json:"result,omitempty"`        // result: 成功结果文本
	Error         string   `json:"error,omitempty"`         // result(error)/error: 失败原因
	ClientVersion string   `json:"client_version,omitempty"` // register
	Capabilities  []string `json:"capabilities,omitempty"`   // register
	Platform      string   `json:"platform,omitempty"`       // register
}
