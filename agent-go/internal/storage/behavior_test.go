package storage

import "testing"

// TestEffectiveMaxToolRounds 未设置/越界收敛到合法范围
func TestEffectiveMaxToolRounds(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 10},  // 未设置 → 默认 10
		{8, 8},   // 正常值
		{50, 50}, // 上限
		{99, 50}, // 越界收敛到 50
		{-5, 1},  // 负数收敛到 1
	}
	for _, c := range cases {
		b := AgentBehavior{MaxToolRounds: c.in}
		if got := b.EffectiveMaxToolRounds(); got != c.want {
			t.Errorf("MaxToolRounds=%d → %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEffectiveMaxOTACOIterations 未设置/越界收敛到合法范围
func TestEffectiveMaxOTACOIterations(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 15},  // 未设置 → 默认 15
		{20, 20}, // 正常值
		{50, 50}, // 上限
		{99, 50}, // 越界收敛到 50
		{-5, 1},  // 负数收敛到 1
	}
	for _, c := range cases {
		b := AgentBehavior{MaxOTACOIterations: c.in}
		if got := b.EffectiveMaxOTACOIterations(); got != c.want {
			t.Errorf("MaxOTACOIterations=%d → %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEffectiveSandboxMode 空/未知 → ask，auto/yolo 原样
func TestEffectiveSandboxMode(t *testing.T) {
	if got := (AgentBehavior{}).EffectiveSandboxMode(); got != "ask" {
		t.Errorf("空模式应回退 ask，实际 %s", got)
	}
	if got := (AgentBehavior{SandboxMode: "yolo"}).EffectiveSandboxMode(); got != "yolo" {
		t.Errorf("yolo 应原样，实际 %s", got)
	}
	if got := (AgentBehavior{SandboxMode: "xxx"}).EffectiveSandboxMode(); got != "ask" {
		t.Errorf("未知模式应回退 ask，实际 %s", got)
	}
}

// TestIsAutoApprove auto/yolo 自动放行
func TestIsAutoApprove(t *testing.T) {
	if (AgentBehavior{}).IsAutoApprove() {
		t.Error("ask 不应自动放行")
	}
	if !(AgentBehavior{SandboxMode: "auto"}).IsAutoApprove() {
		t.Error("auto 应自动放行")
	}
	if !(AgentBehavior{SandboxMode: "yolo"}).IsAutoApprove() {
		t.Error("yolo 应自动放行")
	}
}

// TestEffectiveBackupMode 空 → snapshot，git 原样
func TestEffectiveBackupMode(t *testing.T) {
	if got := (AgentBehavior{}).EffectiveBackupMode(); got != "snapshot" {
		t.Errorf("空模式应回退 snapshot，实际 %s", got)
	}
	if got := (AgentBehavior{BackupMode: "git"}).EffectiveBackupMode(); got != "git" {
		t.Errorf("git 应原样，实际 %s", got)
	}
}
