package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// MasterKeyManager 主密钥管理器（多版本，支持轮换）
// 密钥来源：.env 的 MASTER_KEY_V1 / MASTER_KEY_V2 / ...（base64 编码的 32 字节）
// 加密：用 currentVersion 指定的版本（默认最大存在的版本）
// 解密：按 keyVersion 字段取对应版本（旧数据用旧密钥解）
type MasterKeyManager struct {
	mu            sync.RWMutex
	keys          map[int][]byte // version → 32字节密钥
	currentVersion int           // 当前加密用的版本
}

// NewMasterKeyManager 从环境变量加载主密钥
// 规则：
//   - 扫描 MASTER_KEY_V1 / MASTER_KEY_V2 / ... 直到 V10
//   - currentVersion = MASTER_KEY_VERSION 环境变量（若指定且存在），否则用最大存在的版本
//   - 至少需要 V1，否则返回 error（生产）或用 dev 兜底密钥（开发）
func NewMasterKeyManager(devMode bool) (*MasterKeyManager, error) {
	m := &MasterKeyManager{keys: map[int][]byte{}}

	for v := 1; v <= 10; v++ {
		keyB64 := os.Getenv(fmt.Sprintf("MASTER_KEY_V%d", v))
		if keyB64 == "" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("MASTER_KEY_V%d base64 解码失败: %w", v, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("MASTER_KEY_V%d 长度错误：需要 32 字节（base64 后 44 字符），实际 %d 字节", v, len(key))
		}
		m.keys[v] = key
	}

	if len(m.keys) == 0 {
		if devMode {
			// dev 模式：生成临时兜底密钥（不安全，仅开发测试用）
			devKey := make([]byte, 32)
			if _, err := rand.Read(devKey); err != nil {
				return nil, fmt.Errorf("生成 dev 兜底密钥失败: %w", err)
			}
			m.keys[1] = devKey
			m.currentVersion = 1
			fmt.Println("[WARN][crypto] 未配置 MASTER_KEY_V1，dev 模式使用随机兜底密钥（重启后旧密文无法解密，仅供开发测试）")
			return m, nil
		}
		return nil, errors.New("未配置主密钥（请在 .env 设置 MASTER_KEY_V1，可用 `openssl rand -base64 32` 生成）")
	}

	// 确定 currentVersion
	if cv := os.Getenv("MASTER_KEY_VERSION"); cv != "" {
		v := parseInt(cv)
		if v > 0 && m.keys[v] != nil {
			m.currentVersion = v
		} else {
			return nil, fmt.Errorf("MASTER_KEY_VERSION=%s 指定的版本不存在", cv)
		}
	} else {
		// 默认用最大版本
		for v := range m.keys {
			if v > m.currentVersion {
				m.currentVersion = v
			}
		}
	}

	return m, nil
}

// CurrentVersion 当前加密使用的版本号
func (m *MasterKeyManager) CurrentVersion() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentVersion
}

// Encrypt 加密明文（AES-256-GCM）
// 返回：nonce(12) || ciphertext(含 auth tag)，以及使用的密钥版本
func (m *MasterKeyManager) Encrypt(plaintext string) ([]byte, int, error) {
	m.mu.RLock()
	key, ok := m.keys[m.currentVersion]
	version := m.currentVersion
	m.mu.RUnlock()
	if !ok {
		return nil, 0, errors.New("当前版本主密钥不存在")
	}

	ct, err := encryptWithKey(key, []byte(plaintext))
	if err != nil {
		return nil, 0, err
	}
	return ct, version, nil
}

// Decrypt 解密密文（按指定版本取密钥）
// ct 格式：nonce(12) || ciphertext(含 auth tag)
func (m *MasterKeyManager) Decrypt(ct []byte, keyVersion int) (string, error) {
	m.mu.RLock()
	key, ok := m.keys[keyVersion]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("版本 %d 的主密钥不存在（可能已删除，需重新设置 LLM key）", keyVersion)
	}

	pt, err := decryptWithKey(key, ct)
	if err != nil {
		return "", fmt.Errorf("解密失败: %w", err)
	}
	return string(pt), nil
}

// encryptWithKey 用指定密钥加密（AES-256-GCM）
// 输出格式：nonce(12字节) || ciphertext(含 16 字节 auth tag)
func encryptWithKey(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	// Seal: nonce || ciphertext || tag（GCM 标准）
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return ct, nil
}

// decryptWithKey 用指定密钥解密
func decryptWithKey(key, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ct) < nonceSize {
		return nil, errors.New("密文长度不足（格式错误）")
	}
	nonce, ciphertext := ct[:nonceSize], ct[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("GCM 校验失败（密钥错误或密文被篡改）: %w", err)
	}
	return pt, nil
}

// parseInt 简单解析 int（失败返回 0）
func parseInt(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
