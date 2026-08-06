package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// agentWorkDir agent 文件操作的工作目录（沙箱根）
// 所有文件工具（file_read/file_write/grep/file_list）解析后的路径必须位于此目录内，
// 与 local-agent 自身目录及项目文件隔离，防止 agent 误读写本地 agent 目录外的内容。
// 由 main 启动时 initWorkDir 初始化（-workdir 参数或 AGENT_WORK_DIR 环境变量，默认 ./workspace）。
var agentWorkDir string

// initWorkDir 初始化 agent 工作目录（不存在则创建）
func initWorkDir(dir string) {
	if dir == "" {
		dir = "workspace"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		fmt.Printf("[WARN] 创建工作目录失败: %v\n", err)
	}
	agentWorkDir = abs
	logf("agent 工作目录: %s（文件操作限定于此目录内）", agentWorkDir)
}

// resolvePath 将 agent 传入的 path 解析为工作区内的绝对路径
// 相对路径 → 基于工作区；绝对路径 → 校验必须位于工作区内，越界返回错误（防越权读写）
func resolvePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("缺少参数 path")
	}
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Join(agentWorkDir, p)
	}
	rel, err := filepath.Rel(agentWorkDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越出 agent 工作区(%s)，已拒绝: %s", agentWorkDir, p)
	}
	return abs, nil
}

// logf 简单日志（与 main 的 log 包一致）
func logf(format string, args ...any) {
	log.Printf(format, args...)
}

// handleCommand 分发执行命令（异步调用，支持多指令并发）
func handleCommand(s *session, cmd serverCommand) {
	var result string
	var errMsg string
	status := statusSuccess

	switch cmd.CommandType {
	case cmdFileRead:
		result, errMsg = fileRead(cmd.Args)
	case cmdFileWrite:
		result, errMsg = fileWrite(cmd.Args)
	case cmdGrep:
		result, errMsg = grepFiles(cmd.Args)
	case cmdSandboxExec:
		timeout := time.Duration(cmd.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		result, errMsg = sandboxExec(cmd.Args, timeout)
	case cmdFileList:
		result, errMsg = fileList(cmd.Args)
	default:
		errMsg = fmt.Sprintf("unknown command type: %s", cmd.CommandType)
	}

	if errMsg != "" {
		status = statusError
		result = ""
	}
	s.sendResult(cmd.RequestID, status, result, errMsg)
}

// fileRead 读文件内容
// args: path（必填，工作区内相对路径或绝对路径）, max_size（可选，默认 10000 字符）
func fileRead(args map[string]any) (string, string) {
	path, err := resolvePath(args["path"].(string))
	if err != nil {
		return "", err.Error()
	}
	maxSize := 10000
	if ms, ok := args["max_size"].(float64); ok && ms > 0 {
		maxSize = int(ms)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Sprintf("读取失败: %v", err)
	}
	content := string(data)
	if len(content) > maxSize {
		content = content[:maxSize] + fmt.Sprintf("\n\n... [截断，共 %d 字符，显示前 %d]", len(string(data)), maxSize)
	}
	return content, ""
}

// fileWrite 写文件
// args: path（必填，工作区内相对路径或绝对路径）, content（必填）, mode（可选：write 覆盖/append 追加，默认 write）
func fileWrite(args map[string]any) (string, string) {
	path, err := resolvePath(args["path"].(string))
	if err != nil {
		return "", err.Error()
	}
	content, _ := args["content"].(string)
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "write"
	}
	if path == "" {
		return "", "缺少参数 path"
	}

	// P9: yolo 模式覆盖已有文件前先备份（可回退）+ git 版本控制
	yolo, backupMode := yoloMode(args)
	if yolo && mode != "append" {
		if _, serr := os.Stat(path); serr == nil {
			backupPath(path, "write", backupMode)
		}
	}

	if mode == "append" {
		f, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if openErr != nil {
			return "", fmt.Sprintf("打开失败: %v", openErr)
		}
		defer f.Close()
		_, err = f.WriteString(content)
	} else {
		err = os.WriteFile(path, []byte(content), 0644)
	}
	if err != nil {
		return "", fmt.Sprintf("写入失败: %v", err)
	}
	// P9: yolo + git 模式提交版本
	if yolo {
		yoloAfterHook(backupMode, "yolo file_write: "+path)
	}
	return fmt.Sprintf(`{"status":"ok","path":"%s","mode":"%s","bytes":%d}`, path, mode, len(content)), ""
}

// grepFiles 在文件/目录中搜索匹配行
// args: pattern（必填，正则）, path（必填，工作区内搜索起点）, glob（可选，文件名匹配模式，默认 *）
func grepFiles(args map[string]any) (string, string) {
	pattern, _ := args["pattern"].(string)
	path, err := resolvePath(args["path"].(string))
	if err != nil {
		return "", err.Error()
	}
	glob, _ := args["glob"].(string)
	if glob == "" {
		glob = "*"
	}
	if pattern == "" {
		return "", "缺少参数 pattern"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Sprintf("正则编译失败: %v", err)
	}

	var matches []string
	maxMatches := 100
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无权限的路径
		}
		if d.IsDir() {
			// 跳过隐藏目录和常见依赖目录
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !matchedGlob(filepath.Base(p), glob) {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(path, p)
				if rel == "" {
					rel = p
				}
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(matches) >= maxMatches {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Sprintf("搜索失败: %v", walkErr)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("【grep】无匹配（pattern=%s path=%s）", pattern, path), ""
	}
	header := fmt.Sprintf("【grep】pattern=%s path=%s glob=%s 共 %d 条匹配\n\n", pattern, path, glob, len(matches))
	return header + strings.Join(matches, "\n"), ""
}

func matchedGlob(name, pattern string) bool {
	matched, _ := filepath.Match(pattern, name)
	return matched
}

// sandboxWhitelist 命令白名单（白名单内免审批，白名单外需服务端审批）
// 客户端二次校验，防止服务端被绕过下发恶意命令
var sandboxWhitelist = map[string]bool{
	// 文件查看
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "file": true, "stat": true,
	// 文本处理
	"grep": true, "find": true, "awk": true, "sed": true, "jq": true, "sort": true, "uniq": true,
	// 开发工具
	"git": true, "go": true, "node": true, "python": true, "python3": true, "pip": true, "npm": true,
	// 网络
	"curl": true, "wget": true,
	// 媒体
	"ffmpeg": true,
	// 基础操作
	"echo": true, "pwd": true, "date": true, "mkdir": true, "touch": true, "cp": true, "mv": true,
	// Windows 兼容
	"dir": true, "type": true, "where": true, "findstr": true,
	// P2 扩展：容器与编排（开发场景高频）
	"docker": true, "docker-compose": true, "kubectl": true,
	// P2 扩展：其他常用
	"tar": true, "zip": true, "unzip": true, "which": true, "env": true, "hostname": true,
}

// sandboxExec 执行 CLI 命令
// 安全（硬约束）：白名单校验 + timeout + 环境变量净化 + 输出截断 4000 字符
// args: command（必填，命令字符串）
func sandboxExec(args map[string]any, timeout time.Duration) (string, string) {
	command, _ := args["command"].(string)
	if command == "" {
		return "", "缺少参数 command"
	}

	// P9: yolo 透传标记（跳过白名单 + 删除前备份 + 版本控制）
	yolo, backupMode := yoloMode(args)

	// 白名单校验（取命令第一个 token 的命令名）
	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return "", "空命令"
	}
	cmdName := filepath.Base(tokens[0])
	cmdName = strings.TrimSuffix(cmdName, ".exe") // Windows 兼容
	if !sandboxWhitelist[cmdName] {
		if !yolo {
			return "", fmt.Sprintf("命令 %q 不在白名单内（白名单内免审批，白名单外需服务端审批）", cmdName)
		}
		logf("[yolo] 白名单外命令放行: %q (backup=%s)", command, backupMode)
	}

	// P9: yolo 删除类命令执行前备份目标（可回退）
	if yolo && isDeleteCommand(cmdName) {
		backupDeleteTargets(tokens, backupMode)
	}

	// 工作目录（可选，默认 agent 工作区）
	workdir, _ := args["workdir"].(string)
	if workdir == "" {
		workdir = agentWorkDir
	}

	// 跨平台执行：Windows 用 cmd /c，其他用 sh -c（支持管道/重定向）
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if workdir != "" {
		if abs, err := filepath.Abs(workdir); err == nil {
			if info, err := os.Stat(abs); err == nil && info.IsDir() {
				cmd.Dir = abs
			}
		}
		// workdir 不存在或非目录时忽略，用默认目录
	}

	// 环境变量净化（移除含 KEY/SECRET/TOKEN/PASSWORD 的变量，防泄露到子进程）
	cmd.Env = sanitizeEnv(os.Environ())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]\n" + stderr.String()
	}

	// 输出截断到 4000 字符（硬约束）
	if len(output) > 4000 {
		output = output[:4000] + fmt.Sprintf("\n\n... [输出截断，共 %d 字符，显示前 4000]", len(output))
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Sprintf("命令执行超时（%v）", timeout)
		}
		// P9: yolo 模式下错误不中断（尽力而为，留痕后返回）
		if yolo {
			logf("[yolo] 命令失败但已放行（留痕）: %q err=%v", command, err)
			return output, fmt.Sprintf("命令执行失败（yolo 模式放行）: %v", err)
		}
		return output, fmt.Sprintf("命令执行失败: %v", err)
	}
	// P9: yolo + git 模式提交版本
	if yolo {
		yoloAfterHook(backupMode, "yolo exec: "+command)
	}
	return output, ""
}

// sanitizeEnv 净化环境变量（移除含敏感关键词的变量，防泄露到子进程）
func sanitizeEnv(env []string) []string {
	sensitive := []string{"KEY", "SECRET", "TOKEN", "PASSWORD"}
	var result []string
	for _, e := range env {
		upper := strings.ToUpper(e)
		skip := false
		for _, s := range sensitive {
			if strings.Contains(upper, s) {
				skip = true
				break
			}
		}
		if !skip {
			result = append(result, e)
		}
	}
	return result
}

// fileList 列目录内容（文件名+大小+修改时间）
// args: path（必填）, recursive（可选，默认 false）
func fileList(args map[string]any) (string, string) {
	path, err := resolvePath(args["path"].(string))
	if err != nil {
		return "", err.Error()
	}
	recursive, _ := args["recursive"].(bool)

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Sprintf("读取目录失败: %v", err)
	}

	var lines []string
	maxEntries := 500 // 防止超大目录输出爆炸

	for _, entry := range entries {
		if len(lines) >= maxEntries {
			lines = append(lines, fmt.Sprintf("... [截断，共超过 %d 条]", maxEntries))
			break
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		var sizeStr string
		if entry.IsDir() {
			sizeStr = "<DIR>"
		} else {
			sizeStr = formatSize(info.Size())
		}

		line := fmt.Sprintf("%s\t%s\t%s", entry.Name(), sizeStr, info.ModTime().Format("2006-01-02 15:04:05"))
		lines = append(lines, line)

		// 递归列子目录（深度限制 1 层，避免输出爆炸）
		if recursive && entry.IsDir() {
			subPath := filepath.Join(path, entry.Name())
			subEntries, err := os.ReadDir(subPath)
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if len(lines) >= maxEntries {
					break
				}
				subInfo, err := sub.Info()
				if err != nil {
					continue
				}
				var subSize string
				if sub.IsDir() {
					subSize = "<DIR>"
				} else {
					subSize = formatSize(subInfo.Size())
				}
				lines = append(lines, fmt.Sprintf("  %s/%s\t%s\t%s", entry.Name(), sub.Name(), subSize, subInfo.ModTime().Format("2006-01-02 15:04:05")))
			}
		}
	}

	if len(lines) == 0 {
		return fmt.Sprintf("【list】目录为空 path=%s", path), ""
	}

	header := fmt.Sprintf("【list】path=%s recursive=%v 共 %d 条\n\n", path, recursive, len(lines))
	return header + strings.Join(lines, "\n"), ""
}

// formatSize 格式化文件大小（人类可读）
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2fGB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2fMB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2fKB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
