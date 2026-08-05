package tools

// ctxKey context key 类型（避免与字符串 key 冲突）
type ctxKey string

// CtxKeyUserID 用于在 context 中传递 userID 到工具 Handler
// 工具通过 ctx.Value(CtxKeyUserID).(string) 取 userID（WS 指令路由到对应用户的本地客户端）
// 由 OTACO Run 注入：ctx = context.WithValue(ctx, tools.CtxKeyUserID, userID)
const CtxKeyUserID ctxKey = "user_id"
