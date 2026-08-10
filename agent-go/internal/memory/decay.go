package memory

import (
	"context"
	"log"
	"sync"
	"time"

	"agent-go/internal/storage"
)

// ==================== 记忆衰减(记忆生命周期 P0) + 冷热分层(D P1) + 归档冻结(D P3) ====================
// 每日定时任务:
//   - 03:00 记忆衰减扫描: 更新 effective_importance(跳过归档项目 + manual 豁免 + 30 天宽限)
//   - 03:30 冷热分层迁移: hot→cold(importance<30 或 90 天未访问), 移出 Chroma
//
// 复用 TTLScheduler 的 Ticker 模式, 不引入 cron 依赖。
// 归档冻结: 所有扫描 SQL 均 JOIN projects 过滤 is_archived=false,
//           归档项目记忆不衰减、不迁移、不唤起改 tier。

// DecayScheduler 每日记忆生命周期调度器(衰减 + 分层)
type DecayScheduler struct {
	svc    *Service
	repos  *storage.Repositories
	loc    *time.Location
	stopCh chan struct{}
	mu     sync.Mutex
	running bool
}

// NewDecayScheduler 创建调度器
func NewDecayScheduler(svc *Service, repos *storage.Repositories) *DecayScheduler {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.UTC
	}
	return &DecayScheduler{
		svc:    svc,
		repos:  repos,
		loc:    loc,
		stopCh: make(chan struct{}),
	}
}

// Start 启动调度器(非阻塞, 后台 goroutine)
func (s *DecayScheduler) Start() {
	log.Printf("[decay-scheduler] 启动, 每日 03:00 衰减 / 03:30 分层 (Asia/Shanghai)")
	go s.schedule()
}

// Stop 停止调度器
func (s *DecayScheduler) Stop() {
	close(s.stopCh)
}

// nextRun 计算下次执行时间(今日已过则顺延明日)
// hour=3 → 衰减; hour=3.5(03:30) → 分层
func (s *DecayScheduler) nextRun(hour int, minute int) time.Time {
	now := time.Now().In(s.loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, s.loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// schedule 调度循环: 依次等待 03:00(衰减) 和 03:30(分层)
func (s *DecayScheduler) schedule() {
	for {
		// 找下一个待执行的槽位
		decayAt := s.nextRun(3, 0)
		tierAt := s.nextRun(3, 30)
		var next time.Time
		var kind string
		if decayAt.Before(tierAt) {
			next, kind = decayAt, "decay"
		} else {
			next, kind = tierAt, "tier"
		}
		wait := time.Until(next)
		log.Printf("[decay-scheduler] 等待 %v 后执行[%s] (下次: %s)",
			wait.Round(time.Second), kind, next.In(s.loc).Format("2006-01-02 15:04:05"))

		select {
		case <-time.After(wait):
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if kind == "decay" {
				s.runDecaySweep(ctx)
			} else {
				s.runTierSweep(ctx)
			}
			cancel()
		case <-s.stopCh:
			log.Println("[decay-scheduler] 已停止")
			return
		}
	}
}

// RunDecaySweep 手动触发衰减扫描(供 API/测试调用, 幂等)
func (s *DecayScheduler) RunDecaySweep(ctx context.Context) error {
	return s.runDecaySweep(ctx)
}

// runDecaySweep 衰减扫描: 更新 effective_importance
// 规则(对齐方案 §3.4): tier=hot, source!=manual, last_accessed_at < now-30d
// 公式: effective_importance = GREATEST(10, importance - FLOOR((DATEDIFF(now, last_accessed_at)-30)/7)*3)
// 跳过归档项目(JOIN projects p ON ... p.is_archived=false)
func (s *DecayScheduler) runDecaySweep(ctx context.Context) error {
	if !s.tryStart() {
		log.Println("[decay-scheduler] 跳过本次衰减(手动任务正在运行)")
		return nil
	}
	defer s.finish()

	start := time.Now()
	log.Println("[decay-scheduler] ===== 开始执行记忆衰减扫描 =====")

	// 方案 §3.4 的 SQL, 增加归档项目过滤(记忆生命周期 P3)
	sql := `UPDATE agent_memories m
	        INNER JOIN agent_projects p ON m.project_id = p.id
	        SET m.effective_importance = GREATEST(10,
	              m.importance - FLOOR((DATEDIFF(NOW(), m.last_accessed_at) - 30) / 7) * 3),
	            m.last_decayed_at = NOW()
	        WHERE m.tier = 'hot'
	          AND m.source != 'manual'
	          AND m.last_accessed_at IS NOT NULL
	          AND m.last_accessed_at < DATE_SUB(NOW(), INTERVAL 30 DAY)
	          AND m.deleted_at IS NULL
	          AND p.is_archived = false`

	res := s.repos.DB.WithContext(ctx).Exec(sql)
	if res.Error != nil {
		log.Printf("[decay-scheduler] 衰减扫描失败: %v", res.Error)
		return res.Error
	}
	log.Printf("[decay-scheduler] 衰减扫描完成, 更新 %d 条记忆, 耗时 %v",
		res.RowsAffected, time.Since(start).Round(time.Millisecond))
	return nil
}

// RunTierSweep 手动触发分层扫描(供 API/测试调用)
func (s *DecayScheduler) RunTierSweep(ctx context.Context) error {
	return s.runTierSweep(ctx)
}

// runTierSweep 冷热分层迁移: hot→cold
// 条件(方案 §5.1): effective_importance < 30 OR last_accessed_at <= now-90d
// 步骤: 查候选 → 调 Python /delete 移出 Chroma → UPDATE tier='cold'
// 跳过归档项目 + 已合并(merged_at IS NOT NULL) + 已删除
func (s *DecayScheduler) runTierSweep(ctx context.Context) error {
	if !s.tryStart() {
		log.Println("[decay-scheduler] 跳过本次分层(手动任务正在运行)")
		return nil
	}
	defer s.finish()

	start := time.Now()
	log.Println("[decay-scheduler] ===== 开始执行冷热分层迁移 =====")

	// 1. 查 hot→cold 候选(跳过归档项目)
	var rows []struct {
		ID        string
		ProjectID string
	}
	sql := `SELECT m.id, m.project_id
	        FROM agent_memories m
	        INNER JOIN agent_projects p ON m.project_id = p.id
	        WHERE m.tier = 'hot'
	          AND (m.effective_importance < 30
	               OR (m.last_accessed_at IS NOT NULL AND m.last_accessed_at < DATE_SUB(NOW(), INTERVAL 90 DAY)))
	          AND m.deleted_at IS NULL
	          AND m.merged_at IS NULL
	          AND p.is_archived = false`
	if err := s.repos.DB.WithContext(ctx).Raw(sql).Scan(&rows).Error; err != nil {
		log.Printf("[decay-scheduler] 查询分层候选失败: %v", err)
		return err
	}
	log.Printf("[decay-scheduler] 分层候选 %d 条", len(rows))

	// 2. 逐条迁移: 先删 Chroma 向量(按 project_id 分组批量), 再 UPDATE tier
	moved := 0
	for _, r := range rows {
		// 调 Python /delete 移出 Chroma(单条)
		if _, err := s.svc.client.Delete(ctx, r.ProjectID, []string{r.ID}); err != nil {
			log.Printf("[decay-scheduler] 删除 Chroma 向量失败 id=%s: %v(跳过, 留待下次)", r.ID, err)
			continue
		}
		// UPDATE tier='cold'
		if err := s.repos.DB.WithContext(ctx).Exec(
			"UPDATE agent_memories SET tier='cold' WHERE id=? AND deleted_at IS NULL", r.ID,
		).Error; err != nil {
			log.Printf("[decay-scheduler] 更新 tier 失败 id=%s: %v", r.ID, err)
			continue
		}
		moved++
	}
	log.Printf("[decay-scheduler] 分层迁移完成: %d/%d 条 hot→cold, 耗时 %v",
		moved, len(rows), time.Since(start).Round(time.Millisecond))
	return nil
}

// tryStart 获取执行权(与 TTL 调度器互斥复用 running 标志)
func (s *DecayScheduler) tryStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *DecayScheduler) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}
