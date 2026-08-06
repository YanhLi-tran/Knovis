# Agent 编排评测报告

- 评测时间: 2026-08-06 02:17:30
- agent-go: `http://127.0.0.1:8001`
- 评测集: `scripts/eval/agent_eval.jsonl`
- Case 总数: 35
- LLM-as-Judge: 启用(deepseek)

## 一、整体指标汇总

| 指标 | 数值 |
|------|------|
| Case 总数 | 35 |
| 通过率 | 5.7% |
| 工具选择准确率 | 82.9% |
| 参数填充正确率 | 45.7% |
| 答案事实准确率(Judge) | 51.4% (判定 35 条) |
| 多工具编排顺序 | 66.7% |
| 多轮指代消解 | 0.0% |
| TtFT mean(ms) | 2224.9 |
| TtFT P50/P95(ms) | 1711.4 / 4826.5 |
| 总延迟 mean(ms) | 33225.1 |
| 总延迟 P50/P95(ms) | 10280.2 / 131224.3 |
| Throughput mean(chars/s) | 91.5 |
| TPOT mean(ms/char) | 10.0 |

## 二、按场景分类指标

| 场景 | Case数 | 通过率 | 工具选择 | 参数填充 | 答案准确 | 顺序 | 多轮 |
|------|--------|--------|----------|----------|----------|------|------|
| multi_tool | 7 | 28.6% | 71.4% | 100.0% | 28.6% | 66.7% | - |
| multi_turn | 4 | 0.0% | 100.0% | 75.0% | 75.0% | - | 0.0% |
| single_tool | 24 | 0.0% | 83.3% | 25.0% | 54.2% | - | - |

## 三、时间效率指标(TtFT / Throughput / TPOT)

> - **TtFT**(Time to First Token):请求发出到首个 token(thought streaming 或 output)的延迟,反映首响速度

> - **Throughput**:输出吞吐量,字符/秒,反映整体生成速度

> - **TPOT**(Time Per Output Token):每输出字符耗时(ms/字符),(总延迟-TtFT)/输出字符数

> - 多轮 case 的总延迟/字符数为各轮之和,TtFT 取首轮


| 场景 | TtFT mean(ms) | TtFT P50 | TtFT P95 | 总延迟 mean(ms) | 总延迟 P95 | Throughput(chars/s) | TPOT(ms/char) | 样本数 |
|------|--------------|----------|----------|----------------|-----------|---------------------|---------------|--------|
| multi_tool | 2125.1 | 1681.8 | 3722.6 | 63471.0 | 134247.4 | 81.4 | 13.2 | 6 |
| multi_turn | 2342.3 | 2480.2 | 3664.2 | 15605.1 | 23980.8 | 87.4 | 11.2 | 4 |
| single_tool | 2230.7 | 1536.9 | 5772.2 | 27340.1 | 121436.9 | 93.4 | 9.4 | 22 |

## 四、每条 Case 详情

| ID | 场景 | 查询(截断) | 期望工具 | 实际调用 | 工具选择 | 参数 | 答案 | TtFT(ms) | 总延迟(ms) | chars | 通过 |
|----|------|-----------|----------|----------|----------|------|------|----------|-----------|-------|------|
| 1 | single_tool | 五粮液 2021 年营业收入是多少 | rag_search | rag_search,rag_search,rag_search,rag_search,rag_search | 是 | 否 | 是 | 3398.4 | 15379.2 | 522 | 否 |
| 2 | single_tool | 贵州茅台 2022 年净利润 | rag_search | rag_search,rag_search,rag_search,rag_search,rag_search,web_search | 是 | 否 | 是 | 839.7 | 22953.3 | 651 | 否 |
| 3 | single_tool | 宁德时代 2023 年研发投入多少 | rag_search | rag_search,web_search | 是 | 否 | 是 | 2054.0 | 6721.3 | 740 | 否 |
| 4 | single_tool | 从公司年报里查一下海康威视 2022 年营收 | rag_search | rag_search,rag_search,rag_search | 是 | 否 | 是 | 882.0 | 10280.2 | 514 | 否 |
| 5 | single_tool | 文档库里有没有关于中国平安 2021 年保费收入的 | rag_search | rag_search,rag_search,rag_search | 是 | 否 | 是 | 1060.7 | 21176.4 | 1541 | 否 |
| 6 | single_tool | 对比贵州茅台和五粮液 2022 年的营收 | rag_search | rag_search,rag_search | 是 | 否 | 是 | 1545.5 | 8465.8 | 729 | 否 |
| 7 | single_tool | 贵州茅台 2021 2022 2023 三年营收趋 | rag_search | rag_search,web_search,web_search,web_search | 是 | 否 | 是 | 9081.3 | 11812.8 | 1227 | 否 |
| 8 | single_tool | 五粮液公司全称是什么 | rag_search | - | 否 | 否 | 否 | 1194.5 | 1534.5 | 172 | 否 |
| 9 | single_tool | 今天北京天气怎么样 | get_weather | get_weather | 是 | 否 | 是 | 5869.1 | 6746.1 | 398 | 否 |
| 10 | single_tool | 上海未来三天天气预报 | get_weather_forecast | get_weather | 否 | 否 | 是 | 2159.7 | 3330.1 | 532 | 否 |
| 11 | single_tool | 深圳明天会下雨吗 | get_weather,get_weather_forecast | get_weather | 否 | 否 | 否 | 978.4 | 3838.4 | 334 | 否 |
| 12 | single_tool | 广州的经纬度是多少 | geocode_city | geocode_city | 是 | 否 | 是 | 2326.3 | 2561.2 | 82 | 否 |
| 13 | single_tool | 搜索一下 2026 年最新的 AI 大模型进展 | web_search | web_search,web_search | 是 | 否 | 否 | 893.2 | 9537.5 | 383 | 否 |
| 14 | single_tool | 最近有什么科技新闻 | web_search | web_search | 是 | 否 | 是 | 846.4 | 5200.8 | 1357 | 否 |
| 15 | single_tool | 比亚迪最新的销量数据 | web_search | web_search,web_search | 是 | 否 | 是 | 1528.4 | 9797.0 | 728 | 否 |
| 16 | single_tool | 贵州茅台股票最近股价 | web_search | web_search | 是 | 否 | 是 | 3932.2 | 5341.8 | 542 | 否 |
| 17 | single_tool | 读一下 /workspace/test.txt 这 | file_read | file_read,file_read,sandbox_exec | 是 | 是 | 否 | 2783.7 | 126448.4 | 0 | 否 |
| 18 | single_tool | 在 /workspace 下搜索包含 error  | grep | grep,grep,grep,grep,grep | 是 | 是 | 否 | 2784.0 | 6032.6 | 0 | 否 |
| 19 | single_tool | 列出 /workspace 目录下的文件 | file_list | file_list | 是 | 是 | 否 | 2209.3 | 2677.3 | 185 | 否 |
| 20 | single_tool | 把这段话写到 /workspace/notes.m | file_write | file_write | 是 | 否 | 否 | - | 121322.7 | 0 | 否 |
| 21 | single_tool | 帮我跑一下 ls -la 命令看看当前目录 | sandbox_exec | sandbox_exec | 是 | 是 | 否 | 1077.1 | 121424.8 | 0 | 否 |
| 22 | single_tool | 跑一下 git status 看看仓库状态 | sandbox_exec | sandbox_exec | 是 | 是 | 否 | - | 121439.0 | 0 | 否 |
| 23 | single_tool | 帮我查一下瑞幸咖啡最近的优惠券 | load_skill | web_search | 否 | 否 | 否 | 820.3 | 7886.4 | 1222 | 否 |
| 24 | single_tool | 我想发一条 Knovis 动态 | load_skill | load_skill | 是 | 是 | 否 | 811.8 | 4254.7 | 594 | 否 |
| 25 | multi_tool | 对比公司差旅政策和行业最新标准 | rag_search,web_search | rag_search,web_search,rag_search,file_list,ask_user | 是 | 是 | 否 | 1464.5 | 133352.9 | 0 | 否 |
| 26 | multi_tool | 查一下五粮液 2021 年营收,然后把结果写到 / | rag_search,file_write | rag_search,web_search,rag_search,rag_search,file_write | 是 | 是 | 否 | 2970.1 | 134630.7 | 0 | 否 |
| 27 | multi_tool | 贵州茅台 2022 年净利润是多少,对比行业平均水 | rag_search,web_search | rag_search,web_search,web_search | 是 | 是 | 是 | 1311.0 | 12916.7 | 1500 | 是 |
| 28 | multi_tool | 把五粮液和贵州茅台 2022 年营收对比结果存成  | rag_search,file_write | rag_search,rag_search,file_write | 是 | 是 | 否 | 1132.7 | 130312.0 | 0 | 否 |
| 29 | multi_tool | 今天北京天气怎么样,顺便查一下贵州茅台 2023  | get_weather,rag_search | rag_search,get_weather,rag_search,web_search | 是 | 是 | 是 | 1899.2 | 15319.0 | 716 | 是 |
| 30 | multi_tool | 先看下 /workspace 有哪些文件,然后读第 | file_list,file_read | file_list,file_list,file_list,file_list,file_list | 否 | 是 | 否 | - | 5126.9 | 0 | 否 |
| 31 | multi_tool | 搜索 /workspace 下包含营收的文件,把结 | grep,file_write | file_list,grep,grep,grep,file_list | 否 | 是 | 否 | 3973.5 | 12638.9 | 0 | 否 |
| 32 | multi_turn | 五粮液 2021 年营收是多少 | rag_search | rag_search,rag_search,rag_search,web_search | 是 | 是 | 是 | 3083.2 | 17033.1 | 923 | 否 |
| 33 | multi_turn | 查一下贵州茅台 2022 年净利润 | rag_search | rag_search,rag_search,rag_search,rag_search,web_search | 是 | 是 | 是 | 3766.7 | 25206.9 | 1679 | 否 |
| 34 | multi_turn | 今天上海天气 | get_weather | get_weather | 是 | 否 | 是 | 642.2 | 10911.6 | 879 | 否 |
| 35 | multi_turn | 帮我看下 /workspace 下的文件 | file_list | file_list | 是 | 是 | 否 | 1877.2 | 9268.8 | 1375 | 否 |

## 五、多轮指代消解详情

### Case 32:五粮液 2021 年营收是多少

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 那 2022 年呢 | rag_search | - | 否 | 否 | 2581.9 | 3132.9 | 模型回答内容乱码，无法识别任何关于五粮液2022营收的关键数 |
| R2 | 增长了多少 | - | - | 是 | 是 | 1858.8 | 2555.6 | 回答基于前两轮数据计算了增长额77.60和增长率11.72% |

### Case 33:查一下贵州茅台 2022 年净利润

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 换成海康威视的呢 | rag_search | rag_search,web_search | 是 | 是 | 4995.7 | 6465.8 | 模型回答明确提及海康威视（002415）2022年净利润12 |
| R2 | 这两个谁更高 | - | - | 是 | 否 | 1280.5 | 1868.3 | 模型回答为乱码，未包含任何有效数字或结论，无法满足预期答案特 |

### Case 34:今天上海天气

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 明天呢 | get_weather_forecast | - | 否 | 否 | 1042.8 | 1663.9 | 回答内容乱码且未理解用户查询明天预报，未调用forecast |
| R2 | 那北京呢 | get_weather | get_weather | 是 | 否 | 720.3 | 2648.7 | 模型回答内容乱码且未明确提及北京天气，无法满足理解换城市查北 |

### Case 35:帮我看下 /workspace 下的文件

| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |
|------|------|----------|----------|------|------|----------|-----------|------|
| R1 | 第一个文件内容读一下 | file_read | file_list | 否 | 否 | 2326.8 | 3040.2 | 模型回答为乱码，未读取任何文件内容，也未继承上一轮file_ |
| R2 | 里面搜一下 error 关键词 | grep | grep | 是 | 否 | 1876.5 | 2955.7 | 模型回答未明确在指定文件内执行grep error操作，且内 |

## 六、失败 Case 列表(便于人工归因)

共 33 条失败 case:

| ID | 场景 | 查询 | 失败原因诊断 |
|----|------|------|-------------|
| 1 | single_tool | 五粮液 2021 年营业收入是多少 | 参数填充错误(["参数 query 未满足(包含'五粮液'和'2021'和'营业收入')"]) |
| 2 | single_tool | 贵州茅台 2022 年净利润 | 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'2022'和'净利润')"]) |
| 3 | single_tool | 宁德时代 2023 年研发投入多少 | 参数填充错误(["参数 query 未满足(包含'宁德时代'和'2023'和'研发')"]) |
| 4 | single_tool | 从公司年报里查一下海康威视 2022 年营收 | 参数填充错误(["参数 query 未满足(包含'海康威视'和'2022'和'营收')"]) |
| 5 | single_tool | 文档库里有没有关于中国平安 2021 年保费收入的信息 | 参数填充错误(["参数 query 未满足(包含'中国平安'和'2021'和'保费')"]) |
| 6 | single_tool | 对比贵州茅台和五粮液 2022 年的营收 | 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'五粮液'和'2022')"]) |
| 7 | single_tool | 贵州茅台 2021 2022 2023 三年营收趋势 | 参数填充错误(["参数 query 未满足(包含'贵州茅台'和年份)"]) |
| 8 | single_tool | 五粮液公司全称是什么 | 工具选择错误(miss=['rag_search']); 参数填充错误(["参数 query 未满足(包含'五粮液'和'全称'或'公司名称')"]); 答案不准确(模型回答为乱码，未包含预期答案中的公司全称) |
| 9 | single_tool | 今天北京天气怎么样 | 参数填充错误(['参数 city 未满足(北京)']) |
| 10 | single_tool | 上海未来三天天气预报 | 工具选择错误(miss=['get_weather_forecast']); 参数填充错误(['参数 location 未满足(上海相关经纬度)']) |
| 11 | single_tool | 深圳明天会下雨吗 | 工具选择错误(miss=['get_weather_forecast']); 参数填充错误(['参数 city/location 未满足(深圳相关)']); 答案不准确(回答内容乱码，无法识别天气信息或是否下雨结论。) |
| 12 | single_tool | 广州的经纬度是多少 | 参数填充错误(['参数 city 未满足(广州)']) |
| 13 | single_tool | 搜索一下 2026 年最新的 AI 大模型进展 | 参数填充错误(["参数 query 未满足(包含'AI'或'大模型'和'2026')"]); 答案不准确(回答内容乱码且未提供联网搜索结果摘要，无法验证关键事实。) |
| 14 | single_tool | 最近有什么科技新闻 | 参数填充错误(["参数 query 未满足(包含'科技新闻'或'最新')"]) |
| 15 | single_tool | 比亚迪最新的销量数据 | 参数填充错误(["参数 query 未满足(包含'比亚迪'和'销量')"]) |
| 16 | single_tool | 贵州茅台股票最近股价 | 参数填充错误(["参数 query 未满足(包含'贵州茅台'和'股价')"]) |
| 17 | single_tool | 读一下 /workspace/test.txt 这个文件 | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 18 | single_tool | 在 /workspace 下搜索包含 error 的文件 | 答案不准确(answer 为空); SSE 错误: ������ 5 ������������������������������� |
| 19 | single_tool | 列出 /workspace 目录下的文件 | 答案不准确(模型回答为乱码，未返回任何文件列表信息。) |
| 20 | single_tool | 把这段话写到 /workspace/notes.md 文件里:今天的会 | 参数填充错误(["参数 content 未满足(包含'会议纪要')"]); 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 21 | single_tool | 帮我跑一下 ls -la 命令看看当前目录 | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 22 | single_tool | 跑一下 git status 看看仓库状态 | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 23 | single_tool | 帮我查一下瑞幸咖啡最近的优惠券 | 工具选择错误(miss=['load_skill']); 参数填充错误(['参数 skill_name 未满足(luckin 或 knovis)']); 答案不准确(模型回答未先加载瑞幸skill，而是直接给出优惠券信息，不符合预期流程。) |
| 24 | single_tool | 我想发一条 Knovis 动态 | 答案不准确(模型回答为乱码，未包含load_skill('knovis')或knovis_c) |
| 25 | multi_tool | 对比公司差旅政策和行业最新标准 | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 26 | multi_tool | 查一下五粮液 2021 年营收,然后把结果写到 /workspace/ | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 28 | multi_tool | 把五粮液和贵州茅台 2022 年营收对比结果存成 markdown 文 | 答案不准确(answer 为空); SSE 错误: SSE 读取超时(120s) |
| 30 | multi_tool | 先看下 /workspace 有哪些文件,然后读第一个文件的内容 | 工具选择错误(miss=['file_read']); 答案不准确(answer 为空); 工具顺序错误(["期望顺序['file_read', 'file_list'] 实际['file_list']"]); SSE 错误: ������ 5 ������������������������������� |
| 31 | multi_tool | 搜索 /workspace 下包含营收的文件,把结果保存到 /work | 工具选择错误(miss=['file_write']); 答案不准确(answer 为空); 工具顺序错误(["期望顺序['grep', 'file_write'] 实际['file_list', 'grep']"]); SSE 错误: ������ 5 ������������������������������� |
| 32 | multi_turn | 五粮液 2021 年营收是多少 | 多轮指代消解失败 |
| 33 | multi_turn | 查一下贵州茅台 2022 年净利润 | 多轮指代消解失败 |
| 34 | multi_turn | 今天上海天气 | 参数填充错误(['参数 city 未满足(上海)']); 多轮指代消解失败 |
| 35 | multi_turn | 帮我看下 /workspace 下的文件 | 答案不准确(模型回答内容乱码且未返回目录列表，无法满足预期答案特征。); 多轮指代消解失败 |

## 七、待人工复核项

- 需审批工具(file_write/sandbox_exec)的 case:若本地客户端未连接或审批超时,工具执行会失败,
  答案准确率会受影响但工具选择准确率仍可验证,此类 case 标注「待人工复核」
- load_skill 类 case:依赖 Knovis skill 是否注册,若 skill 未配置则 load_skill 可能失败
- web_search/get_weather 类 case:依赖外部 API 可用性,失败时需区分工具故障与编排错误
- LLM-as-Judge 结果受 deepseek 模型能力影响,边界 case 需人工复核
