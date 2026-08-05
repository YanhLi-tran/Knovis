package info

import "strings"

// joinLines 连接多行文本
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}
