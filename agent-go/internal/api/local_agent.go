package api

import "net/http"

// localAgentVersion local-agent 当前发布版本（与 local-agent 内部 clientVersion 对齐）
// 每次 local-agent 发版时更新
const localAgentVersion = "v0.1.4"

// getLocalAgentInfo GET /agent/local/info
// 返回 local-agent 下载与启动引导信息（供前端"本地 Agent"引导弹窗使用）
// 公开接口（不含用户数据）；下载地址可由 AGENT_DOWNLOAD_BASE_URL 配置
// （自建下载服务器 / 内网部署时覆盖默认的 GitHub Releases 地址）
func (s *Server) getLocalAgentInfo(c *GinCompat) {
	cfg := s.configMgr.GetAppConfig()

	// 平台产物命名与 scripts/build-local-agent.sh + GitHub Actions 一致
	platforms := []H{
		{"os": "windows", "arch": "amd64", "file": "local-agent-windows-amd64.exe", "note": "Windows 10/11 (x64)"},
		{"os": "linux", "arch": "amd64", "file": "local-agent-linux-amd64", "note": "Linux (x64)"},
		{"os": "darwin", "arch": "amd64", "file": "local-agent-darwin-amd64", "note": "macOS (Intel)"},
		{"os": "darwin", "arch": "arm64", "file": "local-agent-darwin-arm64", "note": "macOS (Apple Silicon)"},
	}

	c.JSON(http.StatusOK, H{
		"download_url":   cfg.AgentDownloadBaseURL,
		"version":        localAgentVersion,
		"platforms":      platforms,
		"local_ctrl_addr": "127.0.0.1:17000",
	})
}
