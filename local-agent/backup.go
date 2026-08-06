package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ===== YOLO 模式备份留痕（P9）=====
//
// 沙箱权限模式为 yolo 时，服务端在 sandbox_exec / file_write 的 args 中注入：
//   - _yolo: true          → 本机跳过命令白名单、执行前备份
//   - _backup_mode: snapshot|git → 备份方式（用户可配，默认 snapshot）
//
// snapshot：危险操作（删除/覆盖）前把原文件复制到 workspace/.backup/<类型>/<时间戳>/<相对路径>，
//           可从备份目录手动找回（可回退）。
// git：    workspace 用 git 管理（首次自动 init），每次操作后自动 commit，可 git log/checkout 回退任意版本。
// 留痕：   所有 yolo 操作写入 local-agent 日志（logf）。

// yoloMode 解析 yolo 透传标记（_yolo / _backup_mode）
func yoloMode(args map[string]any) (bool, string) {
	yolo, _ := args["_yolo"].(bool)
	mode, _ := args["_backup_mode"].(string)
	if mode != "git" {
		mode = "snapshot"
	}
	return yolo, mode
}

// backupPath 备份文件或目录（yolo 危险操作前调用）
// snapshot 模式：复制到 .backup/<backupType>/<时间戳>/<相对路径>，返回备份目标路径
// git 模式：无需手动备份（git 提交统一版本管理），返回 ""
func backupPath(absPath, backupType, mode string) string {
	if mode == "git" {
		return "" // git 由 gitCommit 统一管理版本
	}
	// 仅备份工作区内的路径（越界路径沙箱已拒绝，这里兜底跳过）
	rel, err := filepath.Rel(agentWorkDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	ts := time.Now().Format("20060102-150405")
	dst := filepath.Join(agentWorkDir, ".backup", backupType, ts, rel)
	if err := copyPath(absPath, dst); err != nil {
		logf("[yolo] 备份失败 %s → %s: %v", absPath, dst, err)
		return ""
	}
	logf("[yolo] 已备份 %s → %s", absPath, dst)
	return dst
}

// copyPath 递归复制文件或目录（保留目录结构）
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.Walk(src, func(p string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

// copyFile 复制单个文件
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// deleteCommands 删除类命令（yolo 模式下执行前备份目标）
var deleteCommands = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "deltree": true, "remove": true,
	"unlink": true, "rmrf": true,
}

// isDeleteCommand 判断命令是否为删除类
func isDeleteCommand(cmdName string) bool {
	return deleteCommands[cmdName]
}

// backupDeleteTargets 解析删除命令中的目标路径并逐个备份（跳过 - 开头的选项）
func backupDeleteTargets(tokens []string, mode string) {
	if len(tokens) < 2 {
		return
	}
	for _, t := range tokens[1:] {
		if strings.HasPrefix(t, "-") {
			continue
		}
		// 处理通配符/引号：仅对不含通配符的简单路径备份
		if strings.ContainsAny(t, "*?") {
			continue
		}
		p := t
		if !filepath.IsAbs(p) {
			p = filepath.Join(agentWorkDir, p)
		}
		abs := filepath.Clean(p)
		if _, err := os.Stat(abs); err != nil {
			continue // 目标不存在则无需备份
		}
		backupPath(abs, "delete", mode)
	}
}

// gitCommit 提交 workspace 变更（git 模式版本控制，首次自动 init）
// 返回 (stdout, errMsg)；"nothing to commit" 视为成功
func gitCommit(msg string) (string, string) {
	if _, err := os.Stat(filepath.Join(agentWorkDir, ".git")); err != nil {
		// 首次：git init + 忽略 .backup + 初始提交
		runGit("init")
		_ = os.WriteFile(filepath.Join(agentWorkDir, ".gitignore"), []byte(".backup/\n"), 0o644)
		runGit("add", "-A")
		runGit("commit", "-m", "init workspace")
		logf("[yolo] workspace 已初始化 git 版本控制")
	}
	if msg == "" {
		msg = "yolo auto commit"
	}
	runGit("add", "-A")
	out, errMsg := runGit("commit", "-m", msg)
	if errMsg != "" && !strings.Contains(errMsg, "nothing to commit") {
		return out, errMsg
	}
	return out, ""
}

// runGit 在 workspace 内执行 git 命令
func runGit(args ...string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = agentWorkDir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), errb.String()
	}
	return out.String(), ""
}

// yoloAfterHook git 模式操作后统一提交（sandbox_exec / file_write 成功后调用）
func yoloAfterHook(mode, msg string) {
	if mode != "git" {
		return
	}
	if _, errMsg := gitCommit(msg); errMsg != "" {
		log.Printf("[yolo] git commit 失败: %s", errMsg)
	} else {
		logf("[yolo] 已提交版本: %s", msg)
	}
}
