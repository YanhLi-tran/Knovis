package config

import (
	"github.com/zeromicro/go-zero/rest"
)

// RestConfig go-zero rest 服务配置（加载 etc/agent-api.yaml）
// 兼容环境变量优先级：环境变量 > yaml（yaml 中已用 ${VAR:-default} 语法展开）
type RestConfig struct {
	rest.RestConf
	Knovis struct {
		KnovisAPIBaseURL string `yaml:"KnovisAPIBaseURL"`
	} `yaml:"Knovis"`
	MemoryServiceURL string `yaml:"MemoryServiceURL"`
	DocServiceURL    string `yaml:"DocServiceURL"`
	MySQL            struct {
		DataSource string `yaml:"DataSource"`
	} `yaml:"MySQL"`
	Redis struct {
		Host string `yaml:"Host"`
		Pass string `yaml:"Pass"`
		Db   int    `yaml:"Db"`
	} `yaml:"Redis"`
	JWT struct {
		Auth     string `yaml:"Auth"`
		Expire   int64  `yaml:"Expire"`
		Issuer   string `yaml:"Issuer"`
		Audience string `yaml:"Audience"`
	} `yaml:"JWT"`
	DeepSeek struct {
		APIKey  string `yaml:"APIKey"`
		BaseURL string `yaml:"BaseURL"`
		Model   string `yaml:"Model"`
	} `yaml:"DeepSeek"`
	MasterKey struct {
		V1 string `yaml:"V1"`
	} `yaml:"MasterKey"`
}
