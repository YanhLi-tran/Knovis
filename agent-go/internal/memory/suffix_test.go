package memory

import "testing"

func TestStripDynamicSuffix(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
	}{
		{"纯 query（旧数据）", "五粮液2023年营收多少", "五粮液2023年营收多少"},
		{"带记忆后缀", "对比茅台和五粮液\n\n## 记忆上下文\n- 用户档案: 金融从业者\n- 项目记忆: 关注白酒板块", "对比茅台和五粮液"},
		{"带时间后缀", "现在几点了\n\n## 当前时间\n2026-08-19 13:00:00 Tuesday（时区: Asia/Shanghai）", "现在几点了"},
		{"带全部后缀", "帮我总结\n\n## 记忆上下文\n档案\n\n## 当前时间\n13:00\n\n## 上下文状态\n当前 token: 100/64000 (0.2%)", "帮我总结"},
		{"只有时间+状态后缀", "你好\n\n## 当前时间\n13:00\n\n## 上下文状态\n当前 token: 100/64000 (0.2%)", "你好"},
		{"query 本身包含类似标记（不剥离）", "请输出格式 ## 当前时间 开头的文本", "请输出格式 ## 当前时间 开头的文本"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripDynamicSuffix(c.in); got != c.want {
				t.Fatalf("StripDynamicSuffix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
