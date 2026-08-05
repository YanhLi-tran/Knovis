# Agent 编排评测报告

- 评测时间: 2026-08-05 12:37:07
- agent-go: `http://127.0.0.1:8001`
- 评测集: `scripts/eval/agent_eval.jsonl`
- Case 总数: 35
- LLM-as-Judge: 未启用

## 一、整体指标汇总

| 指标 | 数值 |
|------|------|
| Case 总数 | 35 |
| 通过率 | 0.0% |
| 工具选择准确率 | 0.0% |
| 参数填充正确率 | 25.7% |
| 答案事实准确率(Judge) | - (判定 0 条) |
| 多工具编排顺序 | 0.0% |
| 多轮指代消解 | 0.0% |
| 总延迟 mean(ms) | 36.0 |
| 总延迟 P50/P95(ms) | 0.0 / 146.3 |

## 二、按场景分类指标

| 场景 | Case数 | 通过率 | 工具选择 | 参数填充 | 答案准确 | 顺序 | 多轮 |
|------|--------|--------|----------|----------|----------|------|------|
| multi_tool | 7 | 0.0% | 0.0% | 100.0% | - | 0.0% | - |
| multi_turn | 4 | 0.0% | 0.0% | 50.0% | - | - | 0.0% |
| single_tool | 24 | 0.0% | 0.0% | 0.0% | - | - | - |

## 三、时间效率指标(TtFT / Throughput / TPOT)

> - **TtFT**(Time to First Token):请求发出到首个 token(thought streaming 或 output)的延迟,反映首响速度

> - **Throughput**:输出吞吐量,字符/秒,反映整体生成速度

> - **TPOT**(Time Per Output Token):每输出字符耗时(ms/字符),(总延迟-TtFT)/输出字符数

> - 多轮 case 的总延迟/字符数为各轮之和,TtFT 取首轮


| 场景 | TtFT mean(ms) | TtFT P50 | TtFT P95 | 总延迟 mean(ms) | 总延迟 P95 | Throughput(chars/s) | TPOT(ms/char) | 样本数 |
|------|--------------|----------|----------|----------------|-----------|---------------------|---------------|--------|
| multi_tool | - | - | - | 0.0 | 0.0 | - | - | 7 |
| multi_turn | - | - | - | 0.0 | 0.0 | - | - | 4 |
| single_tool | - | - | - | 52.4 | 173.9 | - | - | 24 |

## 四、每条 Case 详情

| ID | 场景 | 查询(截断) | 期望工具 | 实际调用 | 工具选择 | 参数 | 答案 | TtFT(ms) | 总延迟(ms) | chars | 通过 |
|----|------|-----------|----------|----------|----------|------|------|----------|-----------|-------|------|
| 1 | single_tool | 五粮液 2021 年营业收入是多少 | rag_search | - | 否 | 否 | - | - | 252.6 | 0 | 否 |
| 2 | single_tool | 贵州茅台 2022 年净利润 | rag_search | - | 否 | 否 | - | - | 131.3 | 0 | 否 |
| 3 | single_tool | 宁德时代 2023 年研发投入多少 | rag_search | - | 否 | 否 | - | - | 106.5 | 0 | 否 |
| 4 | single_tool | 从公司年报里查一下海康威视 2022 年营收 | rag_search | - | 否 | 否 | - | - | 116.6 | 0 | 否 |
| 5 | single_tool | 文档库里有没有关于中国平安 2021 年保费收入的 | rag_search | - | 否 | 否 | - | - | 114.1 | 0 | 否 |
| 6 | single_tool | 对比贵州茅台和五粮液 2022 年的营收 | rag_search | - | 否 | 否 | - | - | 116.4 | 0 | 否 |
| 7 | single_tool | 贵州茅台 2021 2022 2023 三年营收趋 | rag_search | - | 否 | 否 | - | - | 181.4 | 0 | 否 |
| 8 | single_tool | 五粮液公司全称是什么 | rag_search | - | 否 | 否 | - | - | 120.1 | 0 | 否 |
| 9 | single_tool | 今天北京天气怎么样 | get_weather | - | 否 | 否 | - | - | 119.5 | 0 | 否 |
| 10 | single_tool | 上海未来三天天气预报 | get_weather_forecast | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 11 | single_tool | 深圳明天会下雨吗 | get_weather,get_weather_forecast | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 12 | single_tool | 广州的经纬度是多少 | geocode_city | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 13 | single_tool | 搜索一下 2026 年最新的 AI 大模型进展 | web_search | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 14 | single_tool | 最近有什么科技新闻 | web_search | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 15 | single_tool | 比亚迪最新的销量数据 | web_search | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 16 | single_tool | 贵州茅台股票最近股价 | web_search | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 17 | single_tool | 读一下 /workspace/test.txt 这 | file_read | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 18 | single_tool | 在 /workspace 下搜索包含 error  | grep | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 19 | single_tool | 列出 /workspace 目录下的文件 | file_list | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 20 | single_tool | 把这段话写到 /workspace/notes.m | file_write | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 21 | single_tool | 帮我跑一下 ls -la 命令看看当前目录 | sandbox_exec | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 22 | single_tool | 跑一下 git status 看看仓库状态 | sandbox_exec | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 23 | single_tool | 帮我查一下瑞幸咖啡最近的优惠券 | load_skill | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 24 | single_tool | 我想发一条 Knovis 动态 | load_skill | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 25 | multi_tool | 对比公司差旅政策和行业最新标准 | rag_search,web_search | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 26 | multi_tool | 查一下五粮液 2021 年营收,然后把结果写到 / | rag_search,file_write | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 27 | multi_tool | 贵州茅台 2022 年净利润是多少,对比行业平均水 | rag_search,web_search | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 28 | multi_tool | 把五粮液和贵州茅台 2022 年营收对比结果存成  | rag_search,file_write | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 29 | multi_tool | 今天北京天气怎么样,顺便查一下贵州茅台 2023  | get_weather,rag_search | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 30 | multi_tool | 先看下 /workspace 有哪些文件,然后读第 | file_list,file_read | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 31 | multi_tool | 搜索 /workspace 下包含营收的文件,把结 | grep,file_write | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 32 | multi_turn | 五粮液 2021 年营收是多少 | rag_search | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 33 | multi_turn | 查一下贵州茅台 2022 年净利润 | rag_search | - | 否 | 是 | - | - | 0.0 | 0 | 否 |
| 34 | multi_turn | 今天上海天气 | get_weather | - | 否 | 否 | - | - | 0.0 | 0 | 否 |
| 35 | multi_turn | 帮我看下 /workspace 下的文件 | file_list | - | 否 | 否 | - | - | 0.0 | 0 | 否 |

## 五、多轮指代消解详情

### Case 32:五粮液 2021 年营收是多少

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 那 2022 年呢 | rag_search | - | 否 | - | - | 0.0 | Judge 未启用 |
| R2 | 增长了多少 | - | - | 是 | - | - | 0.0 | Judge 未启用 |

### Case 33:查一下贵州茅台 2022 年净利润

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 换成海康威视的呢 | rag_search | - | 否 | - | - | 0.0 | Judge 未启用 |
| R2 | 这两个谁更高 | - | - | 是 | - | - | 0.0 | Judge 未启用 |

### Case 34:今天上海天气

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 明天呢 | get_weather_forecast | - | 否 | - | - | 0.0 | Judge 未启用 |
| R2 | 那北京呢 | get_weather | - | 否 | - | - | 0.0 | Judge 未启用 |

### Case 35:帮我看下 /workspace 下的文件

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 第一个文件内容读一下 | file_read | - | 否 | - | - | 0.0 | Judge 未启用 |
| R2 | 里面搜一下 error 关键词 | grep | - | 否 | - | - | 0.0 | Judge 未启用 |

## 六、失败 Case 列表(便于人工归因)

共 35 条失败 case:

| ID | 场景 | 查询 | 失败原因诊断 |
|----|------|------|-------------|
| 1 | single_tool | 五粮液 2021 年营业收入是多少 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'五粮液'和'2021'和'营业收入')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 2 | single_tool | 贵州茅台 2022 年净利润 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'2022'和'净利润')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 3 | single_tool | 宁德时代 2023 年研发投入多少 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'宁德时代'和'2023'和'研发')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 4 | single_tool | 从公司年报里查一下海康威视 2022 年营收 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'海康威视'和'2022'和'营收')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 5 | single_tool | 文档库里有没有关于中国平安 2021 年保费收入的信息 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'中国平安'和'2021'和'保费')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 6 | single_tool | 对比贵州茅台和五粮液 2022 年的营收 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'五粮液'和'2022')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 7 | single_tool | 贵州茅台 2021 2022 2023 三年营收趋势 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'贵州茅台'和年份)"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 8 | single_tool | 五粮液公司全称是什么 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'五粮液'和'全称'或'公司名称')"]); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 9 | single_tool | 今天北京天气怎么样 | 工具选择错误(miss=['get_weather']); 参数填充错误(['参数 city 未满足(北京)']); SSE 错误: LLM è°ç¨å¤±è´¥ï¼è¯·æ£æ¥ API Key å |
| 10 | single_tool | 上海未来三天天气预报 | 工具选择错误(miss=['get_weather_forecast']); 参数填充错误(['参数 location 未满足(上海相关经纬度)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 11 | single_tool | 深圳明天会下雨吗 | 工具选择错误(miss=['get_weather', 'get_weather_forecast']); 参数填充错误(['参数 city/location 未满足(深圳相关)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 12 | single_tool | 广州的经纬度是多少 | 工具选择错误(miss=['geocode_city']); 参数填充错误(['参数 city 未满足(广州)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 13 | single_tool | 搜索一下 2026 年最新的 AI 大模型进展 | 工具选择错误(miss=['web_search']); 参数填充错误(["参数 query 未满足(包含'AI'或'大模型'和'2026')"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 14 | single_tool | 最近有什么科技新闻 | 工具选择错误(miss=['web_search']); 参数填充错误(["参数 query 未满足(包含'科技新闻'或'最新')"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 15 | single_tool | 比亚迪最新的销量数据 | 工具选择错误(miss=['web_search']); 参数填充错误(["参数 query 未满足(包含'比亚迪'和'销量')"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 16 | single_tool | 贵州茅台股票最近股价 | 工具选择错误(miss=['web_search']); 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'股价')"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 17 | single_tool | 读一下 /workspace/test.txt 这个文件 | 工具选择错误(miss=['file_read']); 参数填充错误(['参数 path 未满足(/workspace/test.txt)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 18 | single_tool | 在 /workspace 下搜索包含 error 的文件 | 工具选择错误(miss=['grep']); 参数填充错误(['参数 pattern 未满足(error)', '参数 path 未满足(/workspace)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 19 | single_tool | 列出 /workspace 目录下的文件 | 工具选择错误(miss=['file_list']); 参数填充错误(['参数 path 未满足(/workspace)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 20 | single_tool | 把这段话写到 /workspace/notes.md 文件里:今天的会 | 工具选择错误(miss=['file_write']); 参数填充错误(['参数 path 未满足(/workspace/notes.md)', "参数 content 未满足(包含'会议纪要')"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 21 | single_tool | 帮我跑一下 ls -la 命令看看当前目录 | 工具选择错误(miss=['sandbox_exec']); 参数填充错误(['参数 command 未满足(ls -la)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 22 | single_tool | 跑一下 git status 看看仓库状态 | 工具选择错误(miss=['sandbox_exec']); 参数填充错误(['参数 command 未满足(git status)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 23 | single_tool | 帮我查一下瑞幸咖啡最近的优惠券 | 工具选择错误(miss=['load_skill']); 参数填充错误(['参数 skill_name 未满足(luckin 或 knovis)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 24 | single_tool | 我想发一条 Knovis 动态 | 工具选择错误(miss=['load_skill']); 参数填充错误(['参数 skill_name 未满足(knovis)']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 25 | multi_tool | 对比公司差旅政策和行业最新标准 | 工具选择错误(miss=['rag_search', 'web_search']); 工具顺序错误(["期望顺序['rag_search', 'web_search'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 26 | multi_tool | 查一下五粮液 2021 年营收,然后把结果写到 /workspace/ | 工具选择错误(miss=['rag_search', 'file_write']); 工具顺序错误(["期望顺序['rag_search', 'file_write'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 27 | multi_tool | 贵州茅台 2022 年净利润是多少,对比行业平均水平 | 工具选择错误(miss=['rag_search', 'web_search']); 工具顺序错误(["期望顺序['rag_search', 'web_search'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 28 | multi_tool | 把五粮液和贵州茅台 2022 年营收对比结果存成 markdown 文 | 工具选择错误(miss=['rag_search', 'file_write']); 工具顺序错误(["期望顺序['rag_search', 'file_write'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 29 | multi_tool | 今天北京天气怎么样,顺便查一下贵州茅台 2023 年营收 | 工具选择错误(miss=['get_weather', 'rag_search']); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 30 | multi_tool | 先看下 /workspace 有哪些文件,然后读第一个文件的内容 | 工具选择错误(miss=['file_list', 'file_read']); 工具顺序错误(["期望顺序['file_read', 'file_list'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 31 | multi_tool | 搜索 /workspace 下包含营收的文件,把结果保存到 /work | 工具选择错误(miss=['grep', 'file_write']); 工具顺序错误(["期望顺序['file_write', 'grep'] 实际[]"]); SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 32 | multi_turn | 五粮液 2021 年营收是多少 | 工具选择错误(miss=['rag_search']); 多轮指代消解失败; SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 33 | multi_turn | 查一下贵州茅台 2022 年净利润 | 工具选择错误(miss=['rag_search']); 多轮指代消解失败; SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 34 | multi_turn | 今天上海天气 | 工具选择错误(miss=['get_weather']); 参数填充错误(['参数 city 未满足(上海)']); 多轮指代消解失败; SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |
| 35 | multi_turn | 帮我看下 /workspace 下的文件 | 工具选择错误(miss=['file_list']); 参数填充错误(['参数 path 未满足(/workspace)']); 多轮指代消解失败; SSE 错误: HTTP 429: {"error":"已达每日免费额度上限（10 次/天）", |

## 七、待人工复核项

- 需审批工具(file_write/sandbox_exec)的 case:若本地客户端未连接或审批超时,工具执行会失败,
  答案准确率会受影响但工具选择准确率仍可验证,此类 case 标注「待人工复核」
- load_skill 类 case:依赖 Knovis skill 是否注册,若 skill 未配置则 load_skill 可能失败
- web_search/get_weather 类 case:依赖外部 API 可用性,失败时需区分工具故障与编排错误
- LLM-as-Judge 结果受 deepseek 模型能力影响,边界 case 需人工复核
