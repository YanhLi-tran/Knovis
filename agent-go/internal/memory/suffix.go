package memory

import "strings"

// 动态后缀标记（orchestrator.Run 拼接到当前轮 user 消息末尾的内容块起始标记）。
// 设计背景：KV 缓存优化（2026-08-19）将 记忆上下文/当前时间/上下文状态 移出 system
// prompt 拼入 user 消息末尾，且落库内容与发给 LLM 的完全一致（跨请求 token 流前缀
// 稳定）；记忆提取/摘要/标题等消费侧据此剥离后缀，只取用户原话。
const (
	dynamicSuffixMemory  = "\n\n## 记忆上下文"
	dynamicSuffixTime    = "\n\n## 当前时间"
	dynamicSuffixCtxInfo = "\n\n## 上下文状态"
)

// StripDynamicSuffix 剥离 user 消息中的动态后缀（记忆上下文/当前时间/上下文状态），
// 返回干净的用户 query。兼容无后缀的旧数据（找不到任何标记时原样返回）。
func StripDynamicSuffix(content string) string {
	cut := len(content)
	for _, mark := range []string{dynamicSuffixMemory, dynamicSuffixTime, dynamicSuffixCtxInfo} {
		if i := strings.Index(content, mark); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(content[:cut])
}
