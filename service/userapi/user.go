package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"knovis/service/userapi/internal/config"
	"knovis/service/userapi/internal/errs"
	"knovis/service/userapi/internal/handler"
	"knovis/service/userapi/internal/svc"

	"github.com/joho/godotenv"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
)

var configFile = flag.String("f", "etc/user-api.yaml", "the config file")

func main() {
	flag.Parse()

	// 加载 .env(项目根; 从启动目录向上查找), 供 yaml 中 ${VAR} 环境变量展开使用
	loadEnvFile()
	// 为缺失的环境变量提供默认值(.env / 已设置的环境变量优先)
	setDefault("HOST", "0.0.0.0")
	setDefault("PORT", "8080")
	setDefault("DB_HOST", "127.0.0.1")
	setDefault("DB_PORT", "3306")
	setDefault("DB_USER", "root")
	setDefault("DB_PASSWORD", "123456")
	setDefault("DB_NAME", "knovis")
	setDefault("REDIS_HOST", "127.0.0.1:6379")
	setDefault("REDIS_PASSWORD", "")
	setDefault("JWT_SECRET", "knovis-secret-key-change-me")
	setDefault("JWT_EXPIRE", "86400")
	setDefault("JWT_ISSUER", "Knovis")
	setDefault("JWT_AUDIENCE", "agent-go")
	setDefault("SMTP_HOST", "smtp.qq.com")
	setDefault("SMTP_PORT", "587")
	setDefault("SMTP_USER", "")
	setDefault("SMTP_PASSWORD", "")
	setDefault("UPLOAD_DIR", "./uploads")

	var c config.Config
	conf.MustLoad(*configFile, &c, conf.UseEnv())

	// 统一错误响应: {"code": <业务码=HTTP状态码>, "message": "..."}
	httpx.SetErrorHandlerCtx(func(ctx context.Context, err error) (int, any) {
		var be *errs.BizError
		if errors.As(err, &be) {
			return be.Code, map[string]any{"code": be.Code, "message": be.Msg}
		}
		return http.StatusBadRequest, map[string]any{"code": http.StatusBadRequest, "message": err.Error()}
	})

	if err := os.MkdirAll(c.UploadDir, 0o755); err != nil {
		fmt.Printf("创建上传目录失败: %v\n", err)
		os.Exit(1)
	}

	server := rest.MustNewServer(c.RestConf,
		rest.WithUnauthorizedCallback(func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.ErrorCtx(r.Context(), w, errs.New(http.StatusUnauthorized, "未提供认证令牌或令牌无效"))
		}),
		// 上传文件静态服务
		rest.WithFileServer("/uploads", http.Dir(c.UploadDir)),
	)
	defer server.Stop()

	// CORS 中间件: 允许 agent-go 前端(localhost:8001)跨域调用 Knovis 登录/用户/动态接口
	server.Use(func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Client-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next(w, r)
		}
	})

	ctx := svc.NewServiceContext(c)
	handler.RegisterHandlers(server, ctx)

	fmt.Printf("Starting server at %s:%d...\n", c.Host, c.Port)
	server.Start()
}

// setDefault 环境变量缺失时设置默认值
func setDefault(key, val string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, val)
	}
}

// loadEnvFile 从当前工作目录逐级向上查找 .env(通常位于项目根, 与 go.mod 同级)并加载,
// 使服务从任意子目录启动都能读取到项目根的 .env
func loadEnvFile() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			_ = godotenv.Load(envPath)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
