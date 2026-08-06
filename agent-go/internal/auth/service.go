package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"agent-go/internal/crypto"
	"agent-go/internal/storage"
)

// AuthService 鉴权服务（SSO 形态：只校验 Knovis 签发的 JWT，不自管签发）
// 职责：access token 校验 + 黑名单（Logout 主动失效）+ LLM/Knovis token 凭证管理 + 限流
// 注册/登录由用户在 Knovis 侧完成并获取 token，agent-go 不提供注册/登录/刷新接口
type AuthService struct {
	userRepo  *storage.UserRepository
	cache     *storage.Cache          // token 黑名单存 Redis（不可用时降级：登出无法主动失效）
	jwtCfg    *JWTConfig
	masterKey *crypto.MasterKeyManager // P3: 用户 LLM key / Knovis token 加解密
}

// NewAuthService 创建鉴权服务
func NewAuthService(userRepo *storage.UserRepository, cache *storage.Cache, jwtCfg *JWTConfig, masterKey *crypto.MasterKeyManager) *AuthService {
	return &AuthService{userRepo: userRepo, cache: cache, jwtCfg: jwtCfg, masterKey: masterKey}
}

// ==================== P3: LLM key 凭证管理（AES-256-GCM 加密存储）====================

// SetLLMKey 加密存储用户的 LLM key
// 明文 key 不会落库，只存 AES-256-GCM 密文 + 密钥版本（轮换用）
func (s *AuthService) SetLLMKey(ctx context.Context, userID, apiKey string) error {
	if s.masterKey == nil {
		return errors.New("主密钥未初始化，无法加密 LLM key")
	}
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("LLM key 不能为空")
	}
	// Lazy upsert：Knovis 用户首次设置凭证时创建本地凭证记录
	if err := s.userRepo.EnsureCredential(userID); err != nil {
		return fmt.Errorf("初始化用户凭证失败: %w", err)
	}
	ct, version, err := s.masterKey.Encrypt(apiKey)
	if err != nil {
		return fmt.Errorf("加密 LLM key 失败: %w", err)
	}
	if err := s.userRepo.UpdateLLMKey(userID, ct, version); err != nil {
		return fmt.Errorf("存储 LLM key 失败: %w", err)
	}
	log.Printf("[INFO][auth] 更新用户 LLM key（加密存储）userID=%s version=%d", userID, version)
	return nil
}

// GetLLMKey 解密获取用户的 LLM key
// 未设置则返回空字符串（调用方回退到 .env 兜底 key）
func (s *AuthService) GetLLMKey(ctx context.Context, userID string) (string, error) {
	if s.masterKey == nil {
		return "", nil
	}
	u, err := s.userRepo.GetByID(userID)
	if err != nil {
		return "", fmt.Errorf("查询用户失败: %w", err)
	}
	if u == nil || len(u.LLMKeyEncrypted) == 0 {
		return "", nil // 未设置 LLM key
	}
	pt, err := s.masterKey.Decrypt(u.LLMKeyEncrypted, u.LLMKeyVersion)
	if err != nil {
		log.Printf("[WARN][auth] 解密 LLM key 失败 userID=%s version=%d: %v", userID, u.LLMKeyVersion, err)
		return "", nil // 解密失败视为未设置，回退兜底
	}
	return pt, nil
}

// ClearLLMKey 清除用户的 LLM key（用户主动删除）
func (s *AuthService) ClearLLMKey(ctx context.Context, userID string) error {
	return s.userRepo.UpdateLLMKey(userID, nil, 0)
}

// HasLLMKey 检查用户是否已设置 LLM key
func (s *AuthService) HasLLMKey(ctx context.Context, userID string) (bool, error) {
	u, err := s.userRepo.GetByID(userID)
	if err != nil || u == nil {
		return false, err
	}
	return len(u.LLMKeyEncrypted) > 0, nil
}

// ==================== P4: Knovis token 凭证管理（AES-256-GCM 加密存储）====================
// Knovis 读操作 Skill（knovis）按需加载时调用 GetKnovisToken 解密用户 token
// 明文 token 不落库，只存 AES-256-GCM 密文 + 密钥版本

// SetKnovisToken 加密存储用户的 Knovis token
func (s *AuthService) SetKnovisToken(ctx context.Context, userID, token string) error {
	if s.masterKey == nil {
		return errors.New("主密钥未初始化，无法加密 Knovis token")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("Knovis token 不能为空")
	}
	// Lazy upsert：Knovis 用户首次设置凭证时创建本地凭证记录
	if err := s.userRepo.EnsureCredential(userID); err != nil {
		return fmt.Errorf("初始化用户凭证失败: %w", err)
	}
	ct, version, err := s.masterKey.Encrypt(token)
	if err != nil {
		return fmt.Errorf("加密 Knovis token 失败: %w", err)
	}
	if err := s.userRepo.UpdateKnovisToken(userID, ct, version); err != nil {
		return fmt.Errorf("存储 Knovis token 失败: %w", err)
	}
	log.Printf("[INFO][auth] 更新用户 Knovis token（加密存储）userID=%s version=%d", userID, version)
	return nil
}

// GetKnovisToken 解密获取用户的 Knovis token
// 未设置则返回空字符串（Skill 工具应提示用户先设置 token）
func (s *AuthService) GetKnovisToken(ctx context.Context, userID string) (string, error) {
	if s.masterKey == nil {
		return "", nil
	}
	u, err := s.userRepo.GetByID(userID)
	if err != nil {
		return "", fmt.Errorf("查询用户失败: %w", err)
	}
	if u == nil || len(u.KnovisTokenEncrypted) == 0 {
		return "", nil
	}
	pt, err := s.masterKey.Decrypt(u.KnovisTokenEncrypted, u.KnovisTokenVersion)
	if err != nil {
		log.Printf("[WARN][auth] 解密 Knovis token 失败 userID=%s version=%d: %v", userID, u.KnovisTokenVersion, err)
		return "", nil
	}
	return pt, nil
}

// ClearKnovisToken 清除用户的 Knovis token
func (s *AuthService) ClearKnovisToken(ctx context.Context, userID string) error {
	return s.userRepo.UpdateKnovisToken(userID, nil, 0)
}

// HasKnovisToken 检查用户是否已设置 Knovis token
func (s *AuthService) HasKnovisToken(ctx context.Context, userID string) (bool, error) {
	u, err := s.userRepo.GetByID(userID)
	if err != nil || u == nil {
		return false, err
	}
	return len(u.KnovisTokenEncrypted) > 0, nil
}

// ==================== P3: 限流（免费额度 + 用户自带 key 不限）====================

// CheckRateLimit 检查用户是否超出免费额度
// usingOwnKey=true 时直接放行（用户自带 key 不限流）
// Redis 不可用时降级放行（不阻断业务）
// 返回 (remaining, error)：remaining=-1 表示不限流；error != nil 表示超限
func (s *AuthService) CheckRateLimit(ctx context.Context, userID string, usingOwnKey bool) (int, error) {
	if usingOwnKey {
		return -1, nil // 用户自带 key，不限流
	}
	if s.cache == nil || !s.cache.Available() {
		return -1, nil // Redis 不可用，降级放行
	}
	// 查用户配额（默认 5 次/天；环境变量 FREE_QUOTA_PER_DAY 可配置；用户记录字段可 per-user 覆盖）
	quota := getFreeQuotaPerDay()
	if u, err := s.userRepo.GetByID(userID); err == nil && u != nil && u.RateQuotaPerDay > 0 {
		quota = u.RateQuotaPerDay
	}
	if quota <= 0 {
		return 0, errors.New("免费额度已用完")
	}
	// Redis 日计数（key 按天，自然过期重置）
	now := time.Now().UTC()
	key := rateLimitKey(userID, now)
	count, err := s.cache.Incr(ctx, key)
	if err != nil {
		return -1, nil // Redis 错误降级放行
	}
	// 首次计数设置 TTL（到当天 UTC 结束）
	if count == 1 {
		_ = s.cache.Expire(ctx, key, secondsUntilUTCMidnight(now))
	}
	remaining := quota - int(count)
	if remaining < 0 {
		return 0, fmt.Errorf("已达每日免费额度上限（%d 次/天）", quota)
	}
	return remaining, nil
}

// rateLimitKey 构造限流计数 key：agent:rate:{userID}:{YYYYMMDD}
func rateLimitKey(userID string, t time.Time) string {
	return fmt.Sprintf("agent:rate:%s:%04d%02d%02d", userID, t.Year(), t.Month(), t.Day())
}

// getFreeQuotaPerDay 读取免费额度配置（FREE_QUOTA_PER_DAY 环境变量，默认 5 次/天）
// 用户未自带 API key、使用系统兜底 key 时的每日免费对话次数
func getFreeQuotaPerDay() int {
	v := os.Getenv("FREE_QUOTA_PER_DAY")
	if v == "" {
		return 5
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 5
	}
	return n
}

// secondsUntilUTCMidnight 计算到下一个 UTC 午夜的秒数
func secondsUntilUTCMidnight(t time.Time) time.Duration {
	next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
	return next.Sub(t)
}

// ==================== SSO 形态：只校验 + 黑名单（Logout）====================

// Logout 登出：将 access token 加入本地 Redis 黑名单（防 access token 滥用）
// 黑名单 key 优先用 token 的 jti；Knovis token 无 jti 时回退到 token SHA256 摘要。
// 黑名单 TTL = token 剩余有效期（过期后自然失效，无需永久存储）
func (s *AuthService) Logout(ctx context.Context, accessToken string) error {
	if accessToken == "" {
		return nil
	}
	claims, err := s.jwtCfg.ParseToken(accessToken)
	if err != nil {
		return nil // 无效 token 无需加入黑名单
	}
	key := blacklistKey(claims, accessToken)
	ttl := time.Until(claims.ExpiresAt.Time)
	return s.revokeToken(ctx, key, ttl)
}

// IsAccessTokenValid 校验 access token 并检查黑名单（中间件用）
// 仅校验签名/过期/签发者/受众 + 黑名单；Knovis token 无 type claim，不再校验类型
// 返回 claims；任何校验失败返回 error
func (s *AuthService) IsAccessTokenValid(ctx context.Context, token string) (*Claims, error) {
	claims, err := s.jwtCfg.ParseToken(token)
	if err != nil {
		return nil, err
	}
	if s.isTokenRevoked(ctx, blacklistKey(claims, token)) {
		return nil, errors.New("token 已失效，请重新登录")
	}
	return claims, nil
}

// blacklistKey 构造黑名单 key：优先 jti，无 jti 时用 token SHA256 摘要
// token 参数仅在 claims.ID 为空（Knovis 不签发 jti）时作为回退摘要来源
func blacklistKey(claims *Claims, token ...string) string {
	if claims.ID != "" {
		return revokedTokenKey(claims.ID)
	}
	if len(token) > 0 && token[0] != "" {
		return revokedTokenKey("sha256:" + sha256Hex(token[0]))
	}
	return ""
}

// isTokenRevoked 检查黑名单 key 是否存在
// Redis 不可用时返回 false（降级：登出无法主动失效，token 自然过期）
func (s *AuthService) isTokenRevoked(ctx context.Context, key string) bool {
	if s.cache == nil || !s.cache.Available() || key == "" {
		return false
	}
	_, ok, _ := s.cache.GetString(ctx, key)
	return ok
}

// revokeToken 将黑名单 key 写入 Redis，TTL = 剩余有效期
func (s *AuthService) revokeToken(ctx context.Context, key string, ttl time.Duration) error {
	if s.cache == nil || !s.cache.Available() || key == "" {
		return nil
	}
	if ttl <= 0 {
		return nil // 已过期，无需加入黑名单
	}
	return s.cache.SetString(ctx, key, "1", ttl)
}

// revokedTokenKey 构造黑名单 key 前缀
func revokedTokenKey(id string) string {
	return "agent:revoked_token:" + id
}

// sha256Hex 计算 SHA256 摘要的十六进制编码
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
