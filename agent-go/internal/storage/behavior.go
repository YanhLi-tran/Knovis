package storage

// AgentBehavior 用户 Agent 行为设置（P9，来自 UserConfig，覆盖全局默认）
type AgentBehavior struct {
	// MaxToolRounds 连续调用工具轮数上限（0=全局默认10，范围1-50）
	MaxToolRounds int
	// SandboxMode 沙箱权限模式：ask / auto / yolo（空=ask）
	SandboxMode string
	// BackupMode 文件备份模式：snapshot / git（空=snapshot）
	BackupMode string
}

// DefaultMaxToolRounds 连续工具轮数全局默认
const DefaultMaxToolRounds = 10

// EffectiveMaxToolRounds 计算生效的连续工具轮上限（0=未设置 → 默认10，越界收敛到1-50）
func (b AgentBehavior) EffectiveMaxToolRounds() int {
	n := b.MaxToolRounds
	if n == 0 {
		n = DefaultMaxToolRounds
	}
	if n < 1 {
		n = 1
	}
	if n > 50 {
		n = 50
	}
	return n
}

// EffectiveSandboxMode 计算生效的沙箱模式（空 → ask）
func (b AgentBehavior) EffectiveSandboxMode() string {
	switch b.SandboxMode {
	case "auto", "yolo":
		return b.SandboxMode
	default:
		return "ask"
	}
}

// EffectiveBackupMode 计算生效的备份模式（空 → snapshot）
func (b AgentBehavior) EffectiveBackupMode() string {
	if b.BackupMode == "git" {
		return "git"
	}
	return "snapshot"
}

// IsAutoApprove 沙箱模式为 auto/yolo 时需审批工具自动放行
func (b AgentBehavior) IsAutoApprove() bool {
	mode := b.EffectiveSandboxMode()
	return mode == "auto" || mode == "yolo"
}

// Behavior 从 UserConfig 提取 Agent 行为设置（nil 时返回全默认）
func (uc *UserConfig) Behavior() AgentBehavior {
	if uc == nil {
		return AgentBehavior{}
	}
	return AgentBehavior{
		MaxToolRounds: uc.MaxToolRounds,
		SandboxMode:   uc.SandboxMode,
		BackupMode:    uc.BackupMode,
	}
}
