// Command measure 实测三层工具架构的 context 占用（token 数）。
//
// 背景：简历口径「FC 常驻 schema ~300 tokens/工具 vs Skill 元信息 ~25 tokens/工具」
// 此前为估算值，本工具按代码真实 schema + 项目统一的 token 估算规则（token_estimator.go：
// 中文字符 ×0.6、ASCII ×0.3）输出实测数字，供文档/简历引用。
//
// 用法：cd agent-go && go run ./cmd/measure
package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"agent-go/internal/knovis"
	"agent-go/internal/llm"
	"agent-go/internal/rag"
	"agent-go/internal/tools"
	"agent-go/internal/tools/file"
	"agent-go/internal/tools/info"
	"agent-go/internal/tools/sandbox"
	"agent-go/internal/tools/skill"
	"agent-go/internal/tools/skill/skills"
	"agent-go/internal/ws"
)

func main() {
	estimator := llm.NewTokenEstimator()

	// ===== 1) 构建与 main.go 等价的工具注册 =====
	registry := tools.NewRegistry()
	info.RegisterWeatherTools(registry)
	info.RegisterWebSearchTools(registry)
	info.RegisterRAGSearchTools(registry, rag.NewDocClient(""))
	file.RegisterFileTools(registry, ws.NewHub())
	sandbox.RegisterSandboxTools(registry, ws.NewHub())

	skillReg := skill.NewRegistry()
	// 与 main.go 等价：从 skills/ 目录加载文件型 skill + 附加内置 Go 工具
	if err := skillReg.LoadFromDir("skills", 512*1024); err != nil {
		panic(err)
	}
	if err := skillReg.AttachToolBuilders(skills.KnovisSkillName, skills.KnovisToolBuilders(nil, knovis.NewClient(""))); err != nil {
		panic(err)
	}
	if err := skillReg.AttachToolBuilders(skills.KBSummarySkillName, []skill.ToolBuilder{skills.BuildKBListDocs(rag.NewDocClient(""))}); err != nil {
		panic(err)
	}

	// ===== 2) 每轮可见工具定义（buildTools 等价，未加载 skill 时）=====
	defs := registry.ToDefinitions()
	defs = append(defs, tools.AskUserDefinition())
	defs = append(defs, tools.SummarizeHistoryDefinition())
	defs = append(defs, skill.LoadSkillDefinition())

	fmt.Println("========== 分层后：每轮固定注入的 context ==========")
	fmt.Printf("%-28s %10s %10s\n", "工具", "字符数", "tokens")
	fmt.Println("----------------------------------------------------------")

	type row struct {
		name   string
		chars  int
		tokens int
	}
	rows := make([]row, 0, len(defs))
	for _, d := range defs {
		schemaJSON, _ := json.Marshal(d.Function)
		toks := estimator.Estimate(string(schemaJSON))
		rows = append(rows, row{name: d.Function.Name, chars: len(string(schemaJSON)), tokens: toks})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].tokens > rows[j].tokens })
	var fcTotal int
	for _, r := range rows {
		fmt.Printf("%-28s %10d %10d\n", r.name, r.chars, r.tokens)
		fcTotal += r.tokens
	}
	fmt.Println("----------------------------------------------------------")
	fmt.Printf("%-28s %10s %10d\n", "FC 工具合计", "", fcTotal)

	// ===== 3) Skill 注册表块 =====
	skillBlock := skill.BuildSkillRegistryBlock(skillReg.List(""))
	skillBlockTokens := estimator.Estimate(skillBlock)
	metaList := skillReg.List("")
	fmt.Printf("%-28s %10d %10d\n", fmt.Sprintf("Skill 注册表块(%d skill)", len(metaList)), len(skillBlock), skillBlockTokens)
	fmt.Printf("%-28s %10s %10d\n", "工具相关合计(每轮)", "", fcTotal+skillBlockTokens)

	// 每行元信息单独估算
	metaTokens := 0
	for _, m := range metaList {
		line := fmt.Sprintf("- %s: %s（触发: %s）", m.Name, m.Description, m.Trigger)
		metaTokens += estimator.Estimate(line)
	}
	fmt.Printf("%-28s %10s %10d\n", "其中: 单条 skill 元信息行", "", metaTokens)

	// ===== 4) 对比场景：knovis 3 工具若以 FC 常驻 =====
	fmt.Println()
	fmt.Println("========== 对比：knovis 3 工具若做成 FC 常驻（假设场景）==========")
	fmt.Println("（schema 文本取自 skills/knovis.go 现有定义，用于量化分层节省）")
	knovisFC := knovisFCDefinitions()
	knovisFCTotal := 0
	for _, d := range knovisFC {
		schemaJSON, _ := json.Marshal(d.Function)
		toks := estimator.Estimate(string(schemaJSON))
		fmt.Printf("%-28s %10d %10d\n", d.Function.Name, len(string(schemaJSON)), toks)
		knovisFCTotal += toks
	}
	fmt.Printf("%-28s %10s %10d\n", "3 工具 FC 合计", "", knovisFCTotal)
	fmt.Printf("分层节省/轮: %d tokens（%d - %d 元信息）\n", knovisFCTotal-metaTokens, knovisFCTotal, metaTokens)

	// ===== 5) 汇总 =====
	fmt.Println()
	fmt.Println("========== 汇总 ==========")
	fmt.Printf("13 个 FC 工具 schema 实测合计: %d tokens（平均 %.1f tokens/工具）\n",
		fcTotal, float64(fcTotal)/float64(len(defs)))
	fmt.Printf("Skill 注册表块实测: %d tokens（%d 个 skill，含固定引导文案）\n", skillBlockTokens, len(metaList))
	for _, m := range metaList {
		line := fmt.Sprintf("- %s: %s（触发: %s）", m.Name, m.Description, m.Trigger)
		fmt.Printf("  - %s 元信息: %d tokens\n", m.Name, estimator.Estimate(line))
	}
}

// knovisFCDefinitions 构造 knovis 3 个工具以 FC 常驻时的 schema（文本与 skills/knovis.go 一致）
func knovisFCDefinitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_feed",
				Description: "获取 Knovis 动态流（读操作）。返回最新动态列表，分页由 page/page_size 控制。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"page":      map[string]any{"type": "integer", "description": "页码（从 1 开始，默认 1）"},
						"page_size": map[string]any{"type": "integer", "description": "每页条数（默认 10，最大 50）"},
					},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_profile",
				Description: "获取 Knovis 用户资料（读操作）。user_id 为空时查当前登录用户。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"user_id": map[string]any{"type": "string", "description": "目标用户 ID（为空则查当前登录用户）"},
					},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_post",
				Description: "获取 Knovis 动态详情（读操作，浏览数 +1）。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"post_id": map[string]any{"type": "string", "description": "动态 ID"},
					},
					"required": []string{"post_id"},
				},
			},
		},
	}
}
