package storage

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig Redis 配置
type RedisConfig struct {
	Host     string
	Port     string
	Password string
	DB       int
}

// LoadRedisConfig 从环境变量加载 Redis 配置
func LoadRedisConfig() *RedisConfig {
	cfg := &RedisConfig{
		Host: getEnv("REDIS_HOST", "127.0.0.1"),
		Port: getEnv("REDIS_PORT", "6379"),
		Password: getEnv("REDIS_PASSWORD", ""),
	}
	// REDIS_DB 简单解析
	dbStr := getEnv("REDIS_DB", "0")
	db := 0
	for _, c := range dbStr {
		if c < '0' || c > '9' {
			db = 0
			break
		}
		db = db*10 + int(c-'0')
	}
	cfg.DB = db
	return cfg
}

// Cache 缓存层封装（Redis 不可用时自动降级，所有方法 no-op，调用方回退 MySQL）
// 设计：缓存是性能优化，不参与正确性；任何错误都视为 miss，不阻断业务
type Cache struct {
	client *redis.Client
	available bool
}

// NewCache 创建缓存客户端（Redis 不可用时返回降级的 Cache，不报错）
func NewCache(cfg *RedisConfig) *Cache {
	c := &Cache{}
	if cfg == nil {
		return c
	}
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Host + ":" + cfg.Port,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     20,
	})

	// Ping 探活，失败则降级
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("[storage] Redis 不可用，降级直读 MySQL（缓存功能关闭）: %v", err)
		_ = client.Close()
		return c // available=false
	}

	c.client = client
	c.available = true
	log.Printf("[storage] Redis 已连接: %s:%s db=%d", cfg.Host, cfg.Port, cfg.DB)
	return c
}

// Available Redis 是否可用
func (c *Cache) Available() bool {
	return c != nil && c.available
}

// GetJSON 按 key 取 JSON 反序列化到 dest；未命中或不可用返回 (false, nil)
func (c *Cache) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	if !c.Available() {
		return false, nil
	}
	val, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil // 缓存 miss
		}
		log.Printf("[WARN][storage] Redis GetJSON 失败 key=%s 降级直读 MySQL err=%v", key, err)
		return false, err // 其他错误也视为 miss
	}
	if err := json.Unmarshal(val, dest); err != nil {
		log.Printf("[WARN][storage] Redis GetJSON 反序列化失败 key=%s err=%v", key, err)
		return false, err
	}
	return true, nil
}

// SetJSON 序列化 value 存入 key，带 TTL
func (c *Cache) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	if !c.Available() {
		return nil // no-op
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := c.client.Set(ctx, key, b, ttl).Err(); err != nil {
		log.Printf("[WARN][storage] Redis SetJSON 失败 key=%s 降级不阻断 err=%v", key, err)
		return err
	}
	return nil
}

// SetString 存原始字符串
func (c *Cache) SetString(ctx context.Context, key, value string, ttl time.Duration) error {
	if !c.Available() {
		return nil
	}
	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		log.Printf("[WARN][storage] Redis SetString 失败 key=%s 降级不阻断 err=%v", key, err)
		return err
	}
	return nil
}

// GetString 取原始字符串
func (c *Cache) GetString(ctx context.Context, key string) (string, bool, error) {
	if !c.Available() {
		return "", false, nil
	}
	val, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", false, nil
		}
		log.Printf("[WARN][storage] Redis GetString 失败 key=%s 降级直读 MySQL err=%v", key, err)
		return "", false, err
	}
	return val, true, nil
}

// Del 删除 key
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if !c.Available() {
		return nil
	}
	if len(keys) == 0 {
		return nil
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		log.Printf("[WARN][storage] Redis Del 失败 count=%d 降级不阻断 err=%v", len(keys), err)
		return err
	}
	return nil
}

// Incr 原子自增并返回新值（Redis 不可用时返回 0 + error）
func (c *Cache) Incr(ctx context.Context, key string) (int64, error) {
	if !c.Available() {
		return 0, errors.New("redis 不可用")
	}
	val, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		log.Printf("[WARN][storage] Redis Incr 失败 key=%s err=%v", key, err)
		return 0, err
	}
	return val, nil
}

// Expire 设置 key 的过期时间
func (c *Cache) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if !c.Available() {
		return nil
	}
	if err := c.client.Expire(ctx, key, ttl).Err(); err != nil {
		log.Printf("[WARN][storage] Redis Expire 失败 key=%s err=%v", key, err)
		return err
	}
	return nil
}

// Close 关闭连接
func (c *Cache) Close() error {
	if !c.Available() {
		return nil
	}
	return c.client.Close()
}

// 缓存 key 前缀 + TTL 常量（统一管理）
const (
	cacheKeyUserConfig    = "agent:user_config:"       // +userID
	cacheKeyProjectMemories = "agent:project_memories:" // +projectID (top-N 注入缓存)
	cacheKeyProjectInfo    = "agent:project_info:"      // +projectID

	ttlUserConfig     = 30 * time.Minute
	ttlProjectMemories = 10 * time.Minute
	ttlProjectInfo    = 10 * time.Minute
)

// TTL 访问器（供 memory 服务引用，常量本身小写不导出）
func TTLUserConfig() time.Duration      { return ttlUserConfig }
func TTLProjectMemories() time.Duration { return ttlProjectMemories }
func TTLProjectInfo() time.Duration     { return ttlProjectInfo }

// UserConfigCacheKey 构造用户档案缓存 key
func UserConfigCacheKey(userID string) string {
	return cacheKeyUserConfig + userID
}

// ProjectMemoriesCacheKey 构造项目记忆注入缓存 key
func ProjectMemoriesCacheKey(projectID string) string {
	return cacheKeyProjectMemories + projectID
}

// ProjectInfoCacheKey 构造项目信息缓存 key
func ProjectInfoCacheKey(projectID string) string {
	return cacheKeyProjectInfo + projectID
}
