// Package upload 提供图片/视频文件的校验、保存与删除能力。
package upload

import (
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxImageSize 单张图片最大 10MB
	MaxImageSize = 10 << 20
	// MaxVideoSize 单个视频最大 100MB
	MaxVideoSize = 100 << 20
	// MaxImageCount 图片动态最多 9 张
	MaxImageCount = 9
)

var (
	imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}
	videoExts = map[string]bool{".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true}
)

// ReqKey 用于在 context 中传递原始 *http.Request(文件上传场景)。
type ctxKey struct{}

var ReqKey ctxKey

// SaveImages 保存多张图片，返回以 /uploads/ 开头的 URL 列表；任一失败则回滚已保存文件。
func SaveImages(userID uint64, files []*multipart.FileHeader, uploadDir string) ([]string, error) {
	var urls []string
	for _, f := range files {
		url, err := saveOne(userID, f, uploadDir, imageExts, MaxImageSize, "image/")
		if err != nil {
			DeleteFiles(urls, uploadDir)
			return nil, err
		}
		urls = append(urls, url)
	}
	return urls, nil
}

// SaveVideo 保存单个视频，返回以 /uploads/ 开头的 URL。
func SaveVideo(userID uint64, f *multipart.FileHeader, uploadDir string) (string, error) {
	return saveOne(userID, f, uploadDir, videoExts, MaxVideoSize, "video/")
}

// DeleteFiles 删除一批以 /uploads/ 开头的文件(忽略不存在等错误)
// 仅接受 /uploads/ 前缀的 URL, 防止路径穿越
func DeleteFiles(urls []string, uploadDir string) {
	for _, u := range urls {
		if !strings.HasPrefix(u, "/uploads/") {
			continue
		}
		name := strings.TrimPrefix(u, "/uploads/")
		_ = os.Remove(filepath.Join(uploadDir, name))
	}
}

func saveOne(userID uint64, f *multipart.FileHeader, uploadDir string, exts map[string]bool, maxSize int64, prefix string) (string, error) {
	if f.Size > maxSize {
		return "", fmt.Errorf("文件大小超过限制")
	}

	contentType := f.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, prefix) {
		return "", fmt.Errorf("只支持 %s 文件", strings.TrimSuffix(prefix, "/"))
	}

	filename := filepath.Base(f.Filename)
	ext := strings.ToLower(filepath.Ext(filename))
	if !exts[ext] {
		return "", fmt.Errorf("不支持的文件格式")
	}

	// 文件名: {userID}_{unixnano}_{原始文件名}
	savedName := strconv.FormatUint(userID, 10) + "_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "_" + filename
	savePath := filepath.Join(uploadDir, savedName)

	src, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("读取上传文件失败")
	}
	defer src.Close()

	dst, err := os.Create(savePath)
	if err != nil {
		return "", fmt.Errorf("保存文件失败")
	}
	defer dst.Close()

	if _, err := dst.ReadFrom(src); err != nil {
		_ = os.Remove(savePath)
		return "", fmt.Errorf("写入文件失败")
	}

	return "/uploads/" + savedName, nil
}
