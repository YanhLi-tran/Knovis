package auth

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
)

// Claims JWT 自定义 claims（SSO 形态：只校验 Knovis 签发的 token，不自管签发）
// Knovis 签发 token 的 claims 为 userId（数字）、iss、aud、iat、exp；没有 username/type。
// 用户身份/资料等业务字段由 /api/v1/users/:id 查询获取，不再从 token 拿。
type Claims struct {
	UserId int64 `json:"userId"` // Knovis 用户 ID（数字，agent-go 内部字符串化使用）
	jwt.RegisteredClaims
}

// UserIDString 返回字符串化的用户 ID（供 sessions.owner_id 等 varchar 字段使用）
func (c *Claims) UserIDString() string {
	return strconv.FormatInt(c.UserId, 10)
}

// JWTConfig JWT 校验配置（SSO 形态：仅用于校验，不签发）
type JWTConfig struct {
	Secret   string // HS256 密钥（必须与 Knovis 的 JWT_SECRET 一致）
	Issuer   string // 签发者（与 Knovis 的 JWT_ISSUER 一致，如 Knovis）
	Audience string // 受众（与 Knovis 的 JWT_AUDIENCE 一致，如 agent-go）
}

// DefaultJWTConfig 从环境变量构造默认配置
// secret 为空时返回 error（启动时校验）
func DefaultJWTConfig() (*JWTConfig, error) {
	secret := getEnv("JWT_SECRET", "")
	if secret == "" {
		return nil, errors.New("JWT_SECRET 未配置（请在 .env 设置与 Knovis 一致的 HS256 密钥）")
	}
	return &JWTConfig{
		Secret:   secret,
		Issuer:   getEnv("JWT_ISSUER", "Knovis"),
		Audience: getEnv("JWT_AUDIENCE", "agent-go"),
	}, nil
}

// ParseToken 解析并校验 JWT token
// 校验项：签名（HS256）、exp（过期）、iss（签发者）、aud（受众）
// 返回 claims；任何校验失败返回 error
func (c *JWTConfig) ParseToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期签名算法: %v", t.Header["alg"])
		}
		return []byte(c.Secret), nil
	}, jwt.WithIssuer(c.Issuer), jwt.WithAudience(c.Audience))
	if err != nil {
		return nil, fmt.Errorf("token 校验失败: %w", err)
	}
	return claims, nil
}

// getEnv 读取环境变量（auth 包内自用，避免与 storage 包循环依赖）
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
