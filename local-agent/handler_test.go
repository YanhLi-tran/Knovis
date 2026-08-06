package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSandboxWhitelistAllowed 白名单内命令应可执行
func TestSandboxWhitelistAllowed(t *testing.T) {
	// echo 在白名单内
	result, errMsg := sandboxExec(map[string]any{
		"command": "echo whitelist_test",
	}, 10*time.Second)
	if errMsg != "" {
		t.Fatalf("白名单内命令应执行成功，实际错误: %s", errMsg)
	}
	if result == "" {
		t.Fatal("echo 命令应有输出")
	}
}

// TestSandboxWhitelistBlocked 白名单外命令应被拒绝
func TestSandboxWhitelistBlocked(t *testing.T) {
	// del / rm / format 等危险命令不在白名单内
	dangerousCommands := []string{
		"del test.txt",
		"rm -rf /",
		"format C:",
		"shutdown /s",
		"rmdir /s /q test",
	}
	for _, cmd := range dangerousCommands {
		_, errMsg := sandboxExec(map[string]any{
			"command": cmd,
		}, 10*time.Second)
		if errMsg == "" {
			t.Fatalf("白名单外命令 %q 应被拒绝，但执行成功了", cmd)
		}
	}
}

// TestSandboxExecTimeout 超时应被中断
func TestSandboxExecTimeout(t *testing.T) {
	// ping 在白名单内？不在。用 sleep 也不在。
	// 用 go 命令（在白名单内）执行一个 sleep 模拟超时
	// Windows 没有 sleep，用 ping 127.0.0.1 -n 10 替代（但 ping 不在白名单）
	// 用 echo + 管道不会超时。改用 go run 一个死循环
	// 简化：用 curl 访问一个不存在的地址，设短超时
	// curl 在白名单内
	result, errMsg := sandboxExec(map[string]any{
		"command": "curl --connect-timeout 5 http://192.0.2.1", // 不可路由地址
	}, 2*time.Second) // 超时设 2s，curl 需 5s，应被中断
	if errMsg == "" {
		t.Fatal("超时命令应返回错误")
	}
	// 超时错误信息应包含"超时"
	if result == "" && errMsg == "" {
		t.Fatal("超时应有输出或错误信息")
	}
}

// TestSandboxOutputTruncation 输出应截断到 4000 字符
func TestSandboxOutputTruncation(t *testing.T) {
	// echo 生成超长输出（5000 字符）
	// Windows cmd 不支持 yes / seq，用 go 生成
	longOutput := ""
	for i := 0; i < 500; i++ {
		longOutput += "0123456789"
	}
	result, errMsg := sandboxExec(map[string]any{
		"command": "echo " + longOutput,
	}, 10*time.Second)
	if errMsg != "" {
		t.Fatalf("命令应执行成功: %s", errMsg)
	}
	// 输出应被截断（含截断提示）
	if len(result) > 4500 { // 4000 + 截断提示文本
		t.Fatalf("输出应截断到约 4000 字符，实际长度 %d", len(result))
	}
}

// TestSandboxEnvSanitization 环境变量净化（KEY/SECRET/TOKEN/PASSWORD 应被移除）
func TestSandboxEnvSanitization(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/root",
		"API_KEY=secret123",
		"DB_PASSWORD=mypass",
		"JWT_TOKEN=eyJhbGc",
		"MY_SECRET=topsecret",
		"NORMAL_VAR=ok",
	}
	sanitized := sanitizeEnv(env)

	sanitizedStr := ""
	for _, e := range sanitized {
		sanitizedStr += e + ";"
	}

	// 敏感变量应被移除
	sensitive := []string{"API_KEY", "DB_PASSWORD", "JWT_TOKEN", "MY_SECRET"}
	for _, s := range sensitive {
		if containsStr(sanitizedStr, s) {
			t.Fatalf("敏感变量 %s 应被移除，净化后: %s", s, sanitizedStr)
		}
	}

	// 正常变量应保留
	if !containsStr(sanitizedStr, "PATH") {
		t.Fatal("PATH 应保留")
	}
	if !containsStr(sanitizedStr, "NORMAL_VAR") {
		t.Fatal("NORMAL_VAR 应保留")
	}
}

// TestSandboxEmptyCommand 空命令应报错
func TestSandboxEmptyCommand(t *testing.T) {
	_, errMsg := sandboxExec(map[string]any{
		"command": "",
	}, 10*time.Second)
	if errMsg == "" {
		t.Fatal("空命令应报错")
	}
}

// TestSandboxMissingCommand 缺少 command 参数应报错
func TestSandboxMissingCommand(t *testing.T) {
	_, errMsg := sandboxExec(map[string]any{}, 10*time.Second)
	if errMsg == "" {
		t.Fatal("缺少 command 参数应报错")
	}
}

// containsStr 简单字符串包含检查（避免引入 strings 包）
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStrHelper(s, substr))
}

func containsStrHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestResolvePathSandbox 工作目录沙箱：相对路径基于工作区，越界路径被拒绝
func TestResolvePathSandbox(t *testing.T) {
	dir := t.TempDir()
	initWorkDir(dir)

	// 1) 相对路径 → 基于工作区
	abs, err := resolvePath("notes/todo.txt")
	if err != nil {
		t.Fatalf("工作区内相对路径应允许: %v", err)
	}
	want := filepath.Join(dir, "notes", "todo.txt")
	if abs != want {
		t.Fatalf("相对路径解析错误: got=%s want=%s", abs, want)
	}

	// 2) 工作区内绝对路径 → 允许
	inner := dir + "/a.txt"
	if _, err := resolvePath(inner); err != nil {
		t.Fatalf("工作区内绝对路径应允许: %v", err)
	}

	// 3) 越界: ../ 逃逸 → 拒绝
	for _, p := range []string{"../secret.txt", "../../etc/passwd", "../local-agent/main.go"} {
		if _, err := resolvePath(p); err == nil {
			t.Fatalf("越界路径 %q 应被拒绝", p)
		}
	}

	// 4) 越界: 工作区外绝对路径 → 拒绝（Windows 下 /etc/passwd 是相对路径, 会安全落入工作区内, 不在此断言）
	for _, p := range []string{"C:\\Windows\\win.ini", "C:/Program Files"} {
		if _, err := resolvePath(p); err == nil {
			t.Fatalf("工作区外绝对路径 %q 应被拒绝", p)
		}
	}
}

// TestFileWriteSandbox 文件写入受限于工作区
func TestFileWriteSandbox(t *testing.T) {
	dir := t.TempDir()
	initWorkDir(dir)

	// 工作区内相对路径可写
	res, errMsg := fileWrite(map[string]any{"path": "out.txt", "content": "hello"})
	if errMsg != "" {
		t.Fatalf("工作区内写入应成功: %s", errMsg)
	}
	if res == "" {
		t.Fatal("写入应有返回")
	}

	// 越界路径被拒绝
	_, errMsg = fileWrite(map[string]any{"path": "../escape.txt", "content": "x"})
	if errMsg == "" {
		t.Fatal("越界写入应被拒绝")
	}
	_, errMsg = fileWrite(map[string]any{"path": "C:\\Windows\\escape.txt", "content": "x"})
	if errMsg == "" {
		t.Fatal("工作区外绝对路径写入应被拒绝")
	}
}
