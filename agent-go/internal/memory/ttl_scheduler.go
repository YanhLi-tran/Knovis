package memory

import (
	"context"
	"log"
	"sync"
	"time"

	"agent-go/internal/storage"

	"gorm.io/gorm"
)

// TTLScheduler TTL 定时任务调度器
// 每周一 Asia/Shanghai 03:00 执行：
// 1. 扫描超14周未访问的记忆，分批100条迁移到 archive 表 + 删 Chroma 向量（跳过已归档项目）
//    高价值记忆（importance>=80）豁免自动归档，只能手动归档
// 2. 物理删除 archive 表中超30天未恢复的记录
//
// 并发与失败策略：
// - 定时任务与手动触发互斥（running 标志），同时调用时后者直接返回
// - 某批事务失败时跳过继续，全部归档完成后对失败批重试最多 3 次
// - 3 次后仍失败的留待下周定时任务自然重试（主表数据未删，下次 ListExpired 会再查到）
type TTLScheduler struct {
	svc       *Service
	ttl       time.Duration  // 记忆 TTL（14周）
	batchSize int            // 单批处理上限
	loc       *time.Location // Asia/Shanghai 时区
	stopCh    chan struct{}
	mu        sync.Mutex // 保护 running 标志
	running   bool       // 是否正在执行（防止定时+手动并发）
}

// NewTTLScheduler 创建调度器
func NewTTLScheduler(svc *Service) *TTLScheduler {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.UTC // 兜底，理论上不会触发
	}
	return &TTLScheduler{
		svc:       svc,
		ttl:       14 * 7 * 24 * time.Hour, // 14 周
		batchSize: 100,
		loc:       loc,
		stopCh:    make(chan struct{}),
	}
}

// Start 启动调度器（非阻塞，后台 goroutine）
func (s *TTLScheduler) Start() {
	next := s.nextRun()
	log.Printf("[ttl-scheduler] 启动，下次执行时间: %s", next.In(s.loc).Format("2006-01-02 15:04:05"))
	go s.schedule()
}

// Stop 停止调度器
func (s *TTLScheduler) Stop() {
	close(s.stopCh)
}

// nextRun 计算下次周一 03:00 Asia/Shanghai 的时间
func (s *TTLScheduler) nextRun() time.Time {
	now := time.Now().In(s.loc)
	daysUntilMonday := (1 - int(now.Weekday()) + 7) % 7
	// 若今天是周一且已过 03:00，顺延到下周一
	if daysUntilMonday == 0 && now.Hour() >= 3 {
		daysUntilMonday = 7
	}
	return time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 3, 0, 0, 0, s.loc)
}

// schedule 调度循环：等待到下次执行时间 -> runOnce -> 重新计算下次
func (s *TTLScheduler) schedule() {
	for {
		next := s.nextRun()
		wait := time.Until(next)
		log.Printf("[ttl-scheduler] 等待 %v 后执行（下次: %s）",
			wait.Round(time.Second), next.In(s.loc).Format("2006-01-02 15:04:05"))

		select {
		case <-time.After(wait):
			s.runOnce()
		case <-s.stopCh:
			log.Println("[ttl-scheduler] 已停止")
			return
		}
	}
}

// tryStart 尝试获取执行权，返回是否获取成功
// 若已有任务在执行（定时或手动），返回 false
func (s *TTLScheduler) tryStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

// finish 释放执行权
func (s *TTLScheduler) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

// runOnce 定时任务执行入口（调度循环调用）
// 若手动触发正在执行，跳过本次
func (s *TTLScheduler) runOnce() {
	if !s.tryStart() {
		log.Println("[ttl-scheduler] 跳过本次定时执行（手动任务正在运行）")
		return
	}
	defer s.finish()

	start := time.Now()
	log.Println("[ttl-scheduler] ===== 开始执行 TTL 归档任务 =====")

	ctx := context.Background()
	archived, failed := s.archiveExpiredMemories(ctx)
	deleted, _ := s.cleanupExpiredArchives(ctx)

	log.Printf("[ttl-scheduler] ===== 任务完成，耗时 %v，归档记忆 %d 条（失败 %d），清理过期归档 %d 条 =====",
		time.Since(start).Round(time.Millisecond), archived, failed, deleted)
}

// RunOnce 手动触发一次 TTL 归档任务（运维 API 用）
// 返回归档数、失败数、清理数、是否正在执行（running=true 表示有任务在跑，本次未执行）
func (s *TTLScheduler) RunOnce(ctx context.Context) (archived, failed int, cleaned int64, running bool) {
	if !s.tryStart() {
		return 0, 0, 0, true
	}
	defer s.finish()

	start := time.Now()
	log.Println("[ttl-scheduler] ===== 手动触发 TTL 归档任务 =====")

	archived, failed = s.archiveExpiredMemories(ctx)
	cleaned, _ = s.cleanupExpiredArchives(ctx)

	log.Printf("[ttl-scheduler] ===== 手动任务完成，耗时 %v，归档 %d 条（失败 %d），清理 %d 条 =====",
		time.Since(start).Round(time.Millisecond), archived, failed, cleaned)
	return
}

// archiveExpiredMemories 分批归档超期未访问的记忆
// 每批 batchSize 条，每批一个事务，批间微停100ms 让出 CPU
// 某批事务失败时跳过继续，全部归档完成后对失败批重试最多 3 次
// 仍失败的留到下周定时任务自然重试（主表数据未删，下次 ListExpired 会再查到）
func (s *TTLScheduler) archiveExpiredMemories(ctx context.Context) (archived, failed int) {
	threshold := time.Now().UTC().Add(-s.ttl) // last_accessed_at < now - 14周

	var failedBatches [][]storage.Memory // 收集失败批用于重试

	for {
		// 每批拉 batchSize 条（ListExpired 内部已过滤 is_archived=true 的项目 + importance>=80 豁免）
		memories, err := s.svc.repos.Memory.ListExpired(threshold, s.batchSize)
		if err != nil {
			log.Printf("[ttl-scheduler] 查询超期记忆失败: %v", err)
			break
		}
		if len(memories) == 0 {
			break
		}

		n, ok := s.archiveBatch(ctx, memories)
		if ok {
			archived += n
		} else {
			failed += n
			failedBatches = append(failedBatches, memories)
		}

		log.Printf("[ttl-scheduler] 批次处理完成：本批 %d 条（累计归档 %d，失败 %d）", n, archived, failed)

		// 本批不足 batchSize，说明没有更多超期记忆了
		if len(memories) < s.batchSize {
			break
		}

		// 微停100ms 让出 CPU，避免影响在线对话
		time.Sleep(100 * time.Millisecond)
	}

	// 全部归档完成后，对失败批重试最多 3 次
	if len(failedBatches) > 0 {
		archived, failed = s.retryFailedBatches(ctx, failedBatches, archived, failed)
	}

	return
}

// retryFailedBatches 重试失败批次，最多 3 次
// 每次重试遍历所有仍未成功的批次；某批成功则计入 archived，否则保留到下一轮
// 3 次后仍失败的留待下周定时任务自然重试
func (s *TTLScheduler) retryFailedBatches(ctx context.Context, failedBatches [][]storage.Memory, archived, failed int) (int, int) {
	log.Printf("[ttl-scheduler] 开始重试 %d 个失败批次", len(failedBatches))
	for retry := 1; retry <= 3; retry++ {
		var stillFailed [][]storage.Memory
		for _, batch := range failedBatches {
			n, ok := s.archiveBatch(ctx, batch)
			if ok {
				archived += n
				failed -= n
				log.Printf("[ttl-scheduler] 重试第 %d 次：成功归档 %d 条", retry, n)
			} else {
				stillFailed = append(stillFailed, batch)
			}
		}
		failedBatches = stillFailed
		if len(failedBatches) == 0 {
			log.Printf("[ttl-scheduler] 重试第 %d 次后全部成功", retry)
			break
		}
		if retry < 3 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if len(failedBatches) > 0 {
		totalFailed := 0
		for _, b := range failedBatches {
			totalFailed += len(b)
		}
		log.Printf("[ttl-scheduler] 重试 3 次后仍有 %d 条归档失败，留待下周定时任务重试", totalFailed)
	}
	return archived, failed
}

// archiveBatch 归档单批记忆（事务：写 archive 表 + 硬删主表，事务外删 Chroma 向量）
// 返回（批内条数, 是否成功）
// 事务失败时整批回滚，主表数据保留，可重试
func (s *TTLScheduler) archiveBatch(ctx context.Context, memories []storage.Memory) (int, bool) {
	if len(memories) == 0 {
		return 0, true
	}

	// 按 project_id 分组（批量删 Chroma 用），同时构造 archive 记录
	projectMemMap := make(map[string][]string)
	var archives []storage.MemoryArchive
	var ids []string
	now := time.Now().UTC()

	for _, m := range memories {
		archives = append(archives, storage.MemoryArchive{
			ID:                m.ID,
			OriginalProjectID: m.ProjectID,
			OriginalOwnerID:   m.OwnerID,
			Content:           m.Content,
			MemoryType:        m.MemoryType,
			Source:            m.Source,
			ArchivedAt:        now,
			RestoreExpiresAt:  now.Add(30 * 24 * time.Hour), // 30 天可恢复
			CreatedAt:         m.CreatedAt,
		})
		ids = append(ids, m.ID)
		projectMemMap[m.ProjectID] = append(projectMemMap[m.ProjectID], m.ID)
	}

	// 事务：批量写 archive 表 + 硬删主表（必须用 tx，保证原子性）
	err := s.svc.repos.DB.Transaction(func(tx *gorm.DB) error {
		if len(archives) > 0 {
			if err := tx.Create(&archives).Error; err != nil {
				return err
			}
		}
		return tx.Unscoped().Where("id IN ?", ids).Delete(&storage.Memory{}).Error
	})
	if err != nil {
		log.Printf("[ttl-scheduler] 批量归档事务失败（本批 %d 条）: %v", len(memories), err)
		return len(memories), false
	}

	// 事务外：按项目批量删 Chroma 向量（失败不阻断归档结果，仅 log）
	// 归档已成功（主表已删），向量残留不影响正确性，后续可由 GC 清理
	for projectID, memIDs := range projectMemMap {
		if _, err := s.svc.GetClient().Delete(ctx, projectID, memIDs); err != nil {
			log.Printf("[ttl-scheduler] 删除 Chroma 向量失败（project=%s, count=%d）: %v", projectID, len(memIDs), err)
		}
		s.svc.InvalidateProjectCache(ctx, projectID)
	}
	return len(memories), true
}

// cleanupExpiredArchives 物理删除 archive 表中超期未恢复的记录
// 条件：restore_expires_at < now AND restored = false
func (s *TTLScheduler) cleanupExpiredArchives(ctx context.Context) (int64, error) {
	deleted, err := s.svc.repos.MemoryArchive.DeleteExpired(time.Now().UTC())
	if err != nil {
		log.Printf("[ttl-scheduler] 清理过期归档失败: %v", err)
	}
	return deleted, err
}
