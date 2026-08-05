package ws

// WebSocket message types (消息类型)
// Protocol: minimal viable protocol for server↔local-agent communication.
// 多用户高并发场景下，所有消息按 userID 隔离路由，request_id 用于 command/result 配对追踪。
const (
	MsgTypeRegister  = "register"  // client → server: 声明客户端能力（升级后首条，可选）
	MsgTypeHeartbeat = "heartbeat" // 双向: 心跳保活（服务端用 gorilla ping/pong，客户端文本心跳备用）
	MsgTypeCommand   = "command"   // server → client: 下发指令
	MsgTypeResult    = "result"    // client → server: 回传执行结果
	MsgTypeError     = "error"     // client → server: 客户端侧错误报告
)

// Command types (指令类型，对应工具层)
// 与 tools/file、tools/sandbox 的工具名一一对应，客户端按 command_type 分发执行。
const (
	CmdFileRead    = "file_read"
	CmdFileWrite   = "file_write"
	CmdGrep        = "grep"
	CmdSandboxExec = "sandbox_exec"
	CmdFileList    = "file_list" // P2 新增：列目录内容
)

// Result status (结果状态)
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// IncomingMessage 客户端 → 服务端消息
// 统一信封，按 Type 分发：register/heartbeat/result/error
type IncomingMessage struct {
	Type          string   `json:"type"`           // register/heartbeat/result/error
	RequestID     string   `json:"request_id"`     // result/error 时必填，与 command 的 request_id 配对
	Status        string   `json:"status"`         // result: success/error
	Result        string   `json:"result"`         // result: 成功结果文本（工具输出）
	Error         string   `json:"error"`          // result(error)/error: 失败原因
	ClientVersion string   `json:"client_version"` // register: 客户端版本
	Capabilities  []string `json:"capabilities"`   // register: 支持的指令类型列表
	Platform      string   `json:"platform"`       // register: 运行平台（windows/linux/darwin）
}

// OutgoingMessage 服务端 → 客户端消息
// 主要用于 command 下发；heartbeat 用 gorilla 内置 ping，不走此结构
type OutgoingMessage struct {
	Type        string         `json:"type"`         // command
	RequestID   string         `json:"request_id"`   // 指令追踪 ID（全局唯一）
	CommandType string         `json:"command_type"` // file_read/file_write/grep/sandbox_exec
	Args        map[string]any `json:"args"`         // 指令参数（与工具 schema 对应）
	Timeout     int            `json:"timeout"`      // 超时秒数（客户端执行上限）
}

// ResultPayload 工具执行结果（内部传递用）
type ResultPayload struct {
	Status string `json:"status"`
	Result string `json:"result"`
	Error  string `json:"error"`
}
