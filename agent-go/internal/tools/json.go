package tools

import "encoding/json"

// jsonUnmarshal 包装 json.Unmarshal（避免在每个文件重复 import）
func jsonUnmarshal(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}
