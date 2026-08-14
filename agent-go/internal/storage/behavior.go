package storage

// AgentBehavior 用户 Agent 行为设置（P9，来自 UserConfig，覆盖全局默认）
type AgentBehavior struct {
	// MaxToolRounds 连续调用工具轮数上限（0=无限制，正数范围1-50；未配置用户默认15）
	MaxToolRounds int
	// SandboxMode 沙箱权限模式：ask / auto / yolo（空=ask）
	SandboxMode string
	// BackupMode 文件备份模式：snapshot / git（空=snapshot）
	BackupMode string
	// MaxContextLength 用户自定义上下文大小 token数（0=未设置 → 项目级/默认 64000）
	MaxContextLength int
	// MaxOTACOIterations OTACO 总迭代轮数上限（0=无限制，正数范围1-50；未配置用户默认10）
	MaxOTACOIterations int
}

// DefaultMaxToolRounds 连续工具轮数全局默认（未配置用户兜底）
const DefaultMaxToolRounds = 15

// DefaultMaxOTACOIterations OTACO 总迭代轮数全局默认（未配置用户兜底）
const DefaultMaxOTACOIterations = 10

// DefaultMaxContextLength 项目上下文长度默认（token）
const DefaultMaxContextLength = 64000

// MaxContextLengthUpperBound 用户可设置的最大上下文（DeepSeek 1M）
const MaxContextLengthUpperBound = 1048576

// EffectiveMaxToolRounds 计算生效的连续工具轮上限
// 0=无限制（ctx 超时兜底）；正数收敛到1-50
func (b AgentBehavior) EffectiveMaxToolRounds() int {
	n := b.MaxToolRounds
	if n <= 0 {
		return 0 // 无限制
	}
	if n > 50 {
		n = 50
	}
	return n
}

// EffectiveMaxOTACOIterations 计算生效的 OTACO 总迭代轮上限
// 0=无限制（ctx 超时兜底）；正数收敛到1-50
func (b AgentBehavior) EffectiveMaxOTACOIterations() int {
	n := b.MaxOTACOIterations
	if n <= 0 {
		return 0 // 无限制
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

// EffectiveMaxContextLength 计算生效的用户上下文长度（0=未设置 → 返回 fallback；越界收敛到 1000-1M）
// fallback 由调用方传入（项目级 MaxContextLength 或默认 64000），保证优先级：用户 > 项目 > 默认
func (b AgentBehavior) EffectiveMaxContextLength(fallback int) int {
	if b.MaxContextLength <= 0 {
		if fallback <= 0 {
			return DefaultMaxContextLength
		}
		return fallback
	}
	if b.MaxContextLength < 1000 {
		return 1000
	}
	if b.MaxContextLength > MaxContextLengthUpperBound {
		return MaxContextLengthUpperBound
	}
	return b.MaxContextLength
}

// Behavior 从 UserConfig 提取 Agent 行为设置（nil 时返回全默认）
func (uc *UserConfig) Behavior() AgentBehavior {
	if uc == nil {
		return AgentBehavior{}
	}
	return AgentBehavior{
		MaxToolRounds:     uc.MaxToolRounds,
		SandboxMode:       uc.SandboxMode,
		BackupMode:        uc.BackupMode,
		MaxContextLength:  uc.MaxContextLength,
		MaxOTACOIterations: uc.MaxOTACOIterations,
	}
}
