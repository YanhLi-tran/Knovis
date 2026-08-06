package api

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"agent-go/internal/storage"
	"agent-go/internal/tools/skill"
)

// skillNameRe skill 名称合法性：字母/数字开头，仅含字母数字下划线短横线，长度 ≤ 64
var skillNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// maxUserScriptBytes 单个上传脚本大小上限（512KB，与内置 skill 加载上限一致）
const maxUserScriptBytes = 512 * 1024

// buildSKILLMD 组装完整 SKILL.md（frontmatter + 正文），与内置 skill 文件格式一致
func buildSKILLMD(name, description, trigger, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + name + "\n")
	sb.WriteString("description: " + description + "\n")
	if trigger != "" {
		sb.WriteString("trigger: " + trigger + "\n")
	}
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	return sb.String()
}

// uploadUserSkill 上传用户私有 Skill（multipart 表单）
// 字段：name / description / trigger / content_md（正文）+ scripts 文件（可多个，统一存 scripts/<basename>）
// 校验：name 格式合法、content_md 非空、name 不与全局内置/已有用户 skill 冲突（name 全局唯一）
// 成功后注册到 skillReg（OwnerUserID=userID），该用户对话中注册表立即可见、可 load_skill
func (s *Server) uploadUserSkill(c *GinCompat) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少用户身份（user_id）"})
		return
	}
	if s.skillReg == nil || s.repos.UserSkill == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "Skill 服务未启用"})
		return
	}

	name := strings.TrimSpace(c.Request().FormValue("name"))
	description := strings.TrimSpace(c.Request().FormValue("description"))
	trigger := strings.TrimSpace(c.Request().FormValue("trigger"))
	body := strings.TrimSpace(c.Request().FormValue("content_md"))

	// 校验
	if !skillNameRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, H{"error": "name 仅支持字母/数字/下划线/短横线，长度 ≤ 64，且以字母或数字开头"})
		return
	}
	if description == "" {
		c.JSON(http.StatusBadRequest, H{"error": "description 不能为空"})
		return
	}
	if body == "" {
		c.JSON(http.StatusBadRequest, H{"error": "content_md（工作流程正文）不能为空"})
		return
	}
	// name 全局唯一：先查注册表（内置 + 已注册用户 skill）
	if owner := s.skillReg.OwnerOf(name); owner != "" {
		c.JSON(http.StatusConflict, H{"error": fmt.Sprintf("Skill 名称 %s 已被占用（归属 %s）", name, owner)})
		return
	}
	if _, ok := s.skillReg.Get(name); ok { // OwnerOf 为空但存在：内置 skill（OwnerUserID 为空）
		c.JSON(http.StatusConflict, H{"error": fmt.Sprintf("Skill 名称 %s 与系统内置 skill 冲突", name)})
		return
	}

	// 解析 scripts 文件（多文件，统一存 scripts/<basename>）
	scripts, err := s.parseScriptFiles(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, H{"error": "解析脚本失败: " + err.Error()})
		return
	}

	// 存 DB（name 唯一索引兜底并发冲突）
	us := &storage.UserSkill{
		UserID:      userID,
		Name:        name,
		Description: description,
		Trigger:     trigger,
		ContentMD:   buildSKILLMD(name, description, trigger, body),
	}
	if err := us.SetScripts(toStorageScripts(scripts)); err != nil {
		c.JSON(http.StatusInternalServerError, H{"error": "序列化脚本失败: " + err.Error()})
		return
	}
	if err := s.repos.UserSkill.Create(us); err != nil {
		log.Printf("[ERROR][api] 创建用户 skill 失败 userID=%s name=%s err=%v", userID, name, err)
		c.JSON(http.StatusConflict, H{"error": "创建失败，可能名称已存在: " + err.Error()})
		return
	}

	// 注册到 Registry（用户私有，仅 owner 可见）
	s.skillReg.Register(&skill.SkillDefinition{
		Metadata: skill.SkillMetadata{
			Name:        name,
			Description: description,
			Trigger:     trigger,
		},
		Instructions: body,
		Scripts:      scripts,
		OwnerUserID:  userID,
	})
	log.Printf("[INFO][api] 用户上传 skill 成功 userID=%s name=%s scripts=%d", userID, name, len(scripts))
	c.JSON(http.StatusOK, H{"status": "ok", "skill": name})
}

// toStorageScripts skill.SkillScript → storage.ScriptFile（storage 层不依赖 skill 包，避免循环导入）
func toStorageScripts(list []skill.SkillScript) []storage.ScriptFile {
	if len(list) == 0 {
		return nil
	}
	out := make([]storage.ScriptFile, 0, len(list))
	for _, s := range list {
		out = append(out, storage.ScriptFile{Filename: s.Filename, Content: s.Content})
	}
	return out
}

// parseScriptFiles 从 multipart 表单读取 scripts 文件
// 文件名仅取 basename（防路径穿越），统一存 scripts/<basename>；SKILL.md 正文引用该路径
func (s *Server) parseScriptFiles(c *GinCompat) ([]skill.SkillScript, error) {
	if err := c.Request().ParseMultipartForm(10 << 20); err != nil {
		return nil, fmt.Errorf("解析表单失败: %w", err)
	}
	if c.Request().MultipartForm == nil || len(c.Request().MultipartForm.File) == 0 {
		return nil, nil
	}
	var scripts []skill.SkillScript
	for _, headers := range c.Request().MultipartForm.File {
		for _, h := range headers {
			base := filepath.Base(h.Filename)
			if base == "" || base == "." || base == "/" || base == "\\" {
				return nil, fmt.Errorf("非法文件名: %q", h.Filename)
			}
			f, err := h.Open()
			if err != nil {
				return nil, err
			}
			content, err := io.ReadAll(io.LimitReader(f, maxUserScriptBytes+1))
			f.Close()
			if err != nil {
				return nil, err
			}
			if len(content) > maxUserScriptBytes {
				return nil, fmt.Errorf("脚本 %s 超过大小上限 %d bytes", base, maxUserScriptBytes)
			}
			scripts = append(scripts, skill.SkillScript{
				Filename: "scripts/" + base,
				Content:  string(content),
			})
		}
	}
	return scripts, nil
}

// listMySkills 我的 Skill 列表（不含正文/脚本全文，避免响应过大）
func (s *Server) listMySkills(c *GinCompat) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少用户身份（user_id）"})
		return
	}
	if s.repos.UserSkill == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "Skill 服务未启用"})
		return
	}
	list, err := s.repos.UserSkill.ListByUser(userID)
	if err != nil {
		log.Printf("[ERROR][api] 查询用户 skill 列表失败 userID=%s err=%v", userID, err)
		c.JSON(http.StatusInternalServerError, H{"error": "查询失败: " + err.Error()})
		return
	}
	type brief struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Trigger     string `json:"trigger"`
		Scripts     int    `json:"scripts"`
		CreatedAt   string `json:"created_at"`
	}
	out := make([]brief, 0, len(list))
	for _, us := range list {
		scripts, _ := us.ScriptsList()
		out = append(out, brief{
			Name:        us.Name,
			Description: us.Description,
			Trigger:     us.Trigger,
			Scripts:     len(scripts),
			CreatedAt:   us.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	c.JSON(http.StatusOK, H{"skills": out})
}

// deleteUserSkill 删除我的 Skill（Registry 校验归属 + DB 双删）
func (s *Server) deleteUserSkill(c *GinCompat) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少用户身份（user_id）"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, H{"error": "缺少 skill 名称"})
		return
	}
	if s.skillReg == nil || s.repos.UserSkill == nil {
		c.JSON(http.StatusServiceUnavailable, H{"error": "Skill 服务未启用"})
		return
	}

	// 先 Registry 移除（校验归属：非本人 skill 或全局内置 skill 拒绝）
	if err := s.skillReg.Unregister(name, userID); err != nil {
		c.JSON(http.StatusForbidden, H{"error": err.Error()})
		return
	}
	// 再删 DB（幂等）
	if err := s.repos.UserSkill.Delete(userID, name); err != nil {
		log.Printf("[ERROR][api] 删除用户 skill 失败 userID=%s name=%s err=%v", userID, name, err)
		c.JSON(http.StatusInternalServerError, H{"error": "删除失败: " + err.Error()})
		return
	}
	log.Printf("[INFO][api] 用户删除 skill userID=%s name=%s", userID, name)
	c.JSON(http.StatusOK, H{"status": "ok", "skill": name})
}
