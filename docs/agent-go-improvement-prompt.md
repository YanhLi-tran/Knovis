# agent-go 对接改进提示词

> 用途：将本提示词直接发送给负责改进 agent-go 的 Agent，使其基于 Knovis 的现状完成对接改造。
> 注意：本文件不含本机路径与真实环境变量值，配置项一律以"与 Knovis 保持一致"表述，具体值由双方线下同步。

```markdown
【任务】基于 Knovis 项目改进 agent-go 的对接

【背景】
- Knovis 是基于 go-zero 框架的「用户 + 动态」数据服务，作为业务数据 owner 对外提供 REST 接口；
  社交互动模块（点赞/收藏/评论/关注/私信）不在 Knovis 范围内，不实现。
- agent-go 是 Agent 系统。目标形态：agent-go 只校验 Knovis 签发的 JWT、不再自行签发；
  通过 REST 调用 Knovis 查询用户与动态，实现业务数据与 Agent 解耦。

【必读资料】
1. Knovis 仓库：https://github.com/YanhLi-tran/Knovis.git（本地克隆后阅读）
   - 必读 README.md 的「供 Agent 对接的接口清单」与「Agent 对接改造清单（供 agent-go 重构参考）」两节
   - 接口描述文件 api/user.api、配置 service/userapi/etc/user-api.yaml
2. agent-go 项目（本地路径由你方获取，以下为相对仓库根的必读文件）：
   - internal/auth/jwt.go            （当前自管签发 JWT，已预留 SSO 迁移注释）
   - internal/tools/skill/skills/aiwallhub.go （当前 Skill 定义，含写操作工具）
   - internal/api/auth_api.go        （/auth/me、/auth/aiwallhub-token 等）
   - internal/config/config.go       （JWT 与 AIWALLHUB_API_BASE_URL 配置）

【改造要求】
1. JWT 切换为「只校验不签发」：
   - 停止使用 GenerateToken 自管签发；注册/登录改由用户在 Knovis 侧完成并获取 token。
   - 配置对齐（.env / config）：JWT_SECRET 与 Knovis 的 JWT_SECRET 保持一致（具体值线下同步，不得写入仓库或提交到 GitHub）；
     JWT_ISSUER=Knovis，JWT_AUDIENCE=agent-go。
   - claim 差异（关键）：Knovis 签发 token 的 claims 为 userId（数字）、iss、aud、iat、exp，
     没有 user_id/username/type。用户 ID 必须从 userId 解析，username 等业务字段改由接口查询获取。
2. Skill 只读化（internal/tools/skill/skills/aiwallhub.go）：
   - 删除全部写工具：aiwallhub_create_post、aiwallhub_delete_post、aiwallhub_comment_post、
     aiallhub_like_post、aiwallhub_unlike_post、aiwallhub_follow_user、aiwallhub_unfollow_user。
   - 保留并修正读工具：
     - aiwallhub_get_feed：分页参数由 limit/cursor 改为 page/page_size（与 Knovis 一致）
     - aiwallhub_get_profile：保持不变（/api/v1/profile[/:user_id]）
   - 新增读工具：
     - aiwallhub_get_post：GET /api/v1/posts/:id（动态详情）
3. 端点契约以 Knovis README「供 Agent 对接的接口清单」为准：
   - GET /api/v1/users/:id、GET /api/v1/feed、GET /api/v1/profile、GET /api/v1/profile/:user_id、GET /api/v1/posts/:id
4. 配置接入：AIWALLHUB_API_BASE_URL 环境变量指向 Knovis 服务地址（如 http://127.0.0.1:8080，具体以部署环境为准）。
5. token 透传：保留 PUT /auth/aiwallhub-token 加密存储用户 Knovis token 的机制，
   Skill 调用 Knovis 时作为 Authorization: Bearer <token> 透传。
6. /auth/me：改为调用 Knovis GET /api/v1/users/:id（使用 token 中的 userId）透传用户资料。

【验证标准】
- 端到端联调通过：Knovis /login 签发 token → agent-go 用该 token 校验成功 →
  aiwallhub_get_feed / aiwallhub_get_profile / aiwallhub_get_post 均正常返回。
- Knovis 错误响应格式为 {"code": <HTTP状态码>, "message": "..."}，需正确处理并透出可读信息。
- 代码中不存在任何点赞/评论/关注/私信相关逻辑。

【约束】
- 不得修改 Knovis 仓库代码；如确需调整接口契约，产出差异清单供双方协商，不得单方面改。
- 保持 agent-go 现有架构、目录结构与代码风格，改动需可编译、可测试。
- 所有环境变量（JWT_SECRET、SMTP、DB 等）只通过本地 .env 配置，严禁写入代码或提交到任何远程仓库。
- 输出：改动清单 + 关键文件 diff 说明 + 联调验证结果。
```
