package memory

import (
	"context"
	"log"
	"sync"
	"time"

	"agent-go/internal/storage"
)

// 消息 TTL 相关常量（滑动窗口压缩后的历史消息清理）
const (
	// MessageTTL 已压缩消息的软删阈值
	// 与用户对齐："压缩后一周未恢复的消息软删"
	// 起算点：restored_at 有值用 restored_at，否则用 summarized_at
	// 用户每次"恢复查看"会重置 restored_at = now，相当于再给 7 天窗口期
	MessageTTL = 7 * 24 * time.Hour

	// MessageTTLBatchSize 单批拉取上限，避免一次拉太多影响在线对话
	MessageTTLBatchSize = 200
)

// MessageTTLScheduler 消息 TTL 定时任务调度器
// 每周一 Asia/Shanghai 04:00 执行（与记忆 TTL 03:00 错开，避免同时峰值）：
//   - 软删已压缩（summarized=true）且超过 7 天未恢复查看的消息
//   - 起算点：restored_at 有值用 restored_at，否则用 summarized_at
//   - 软删后 GetBySessionID / GetRecentBySessionID 等查询自动过滤
//
// 并发保护：running 标志 + sync.Mutex，与手动触发互斥
// 失败重试：单批失败跳过继续，整轮结束后对失败批重试最多 3 次，仍失败留待下周自然重试
type MessageTTLScheduler struct {
	repos     *storage.Repositories
	ttl       time.Duration
	batchSize int
	loc       *time.Location
	stopCh    chan struct{}
	mu        sync.Mutex
	running   bool
}

// NewMessageTTLScheduler 创建调度器
func NewMessageTTLScheduler(repos *storage.Repositories) *MessageTTLScheduler {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.UTC
	}
	return &MessageTTLScheduler{
		repos:     repos,
		ttl:       MessageTTL,
		batchSize: MessageTTLBatchSize,
		loc:       loc,
		stopCh:    make(chan struct{}),
	}
}

// Start 启动调度器（非阻塞，后台 goroutine）
func (s *MessageTTLScheduler) Start() {
	next := s.nextRun()
	log.Printf("[msg-ttl-scheduler] 启动，下次执行时间: %s", next.In(s.loc).Format("2006-01-02 15:04:05"))
	go s.schedule()
}

// Stop 停止调度器
func (s *MessageTTLScheduler) Stop() {
	close(s.stopCh)
}

// nextRun 计算下次周一 04:00 Asia/Shanghai 的时间
func (s *MessageTTLScheduler) nextRun() time.Time {
	now := time.Now().In(s.loc)
	daysUntilMonday := (1 - int(now.Weekday()) + 7) % 7
	// 若今天是周一且已过 04:00，顺延到下周一
	if daysUntilMonday == 0 && now.Hour() >= 4 {
		daysUntilMonday = 7
	}
	return time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 4, 0, 0, 0, s.loc)
}

// schedule 调度循环：等待到下次执行时间 -> runOnce -> 重新计算下次
func (s *MessageTTLScheduler) schedule() {
	for {
		next := s.nextRun()
		wait := time.Until(next)
		log.Printf("[msg-ttl-scheduler] 等待 %v 后执行（下次: %s）",
			wait.Round(time.Second), next.In(s.loc).Format("2006-01-02 15:04:05"))

		select {
		case <-time.After(wait):
			s.runOnce()
		case <-s.stopCh:
			log.Println("[msg-ttl-scheduler] 已停止")
			return
		}
	}
}

// tryStart 尝试获取执行权，返回是否获取成功
// 若已有任务在执行（定时或手动），返回 false
func (s *MessageTTLScheduler) tryStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

// finish 释放执行权
func (s *MessageTTLScheduler) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

// runOnce 定时任务执行入口
func (s *MessageTTLScheduler) runOnce() {
	if !s.tryStart() {
		log.Println("[msg-ttl-scheduler] 跳过本次定时执行（手动任务正在运行）")
		return
	}
	defer s.finish()

	start := time.Now()
	log.Println("[msg-ttl-scheduler] ===== 开始执行消息 TTL 软删任务 =====")
	ctx := context.Background()
	deleted := s.softDeleteExpiredMessages(ctx)
	log.Printf("[msg-ttl-scheduler] ===== 任务完成，耗时 %v，软删 %d 条 =====",
		time.Since(start).Round(time.Millisecond), deleted)
}

// RunOnce 手动触发一次消息 TTL 软删任务（运维 API 用）
// 返回（软删条数，是否正在执行）
// running=true 表示有任务在跑，本次未执行
func (s *MessageTTLScheduler) RunOnce(ctx context.Context) (deleted int64, running bool) {
	if !s.tryStart() {
		return 0, true
	}
	defer s.finish()

	start := time.Now()
	log.Println("[msg-ttl-scheduler] ===== 手动触发消息 TTL 软删任务 =====")
	deleted = s.softDeleteExpiredMessages(ctx)
	log.Printf("[msg-ttl-scheduler] ===== 手动任务完成，耗时 %v，软删 %d 条 =====",
		time.Since(start).Round(time.Millisecond), deleted)
	return
}

// softDeleteExpiredMessages 分批软删超期已压缩消息
// 单批事务失败跳过继续，整轮结束后对失败批重试最多 3 次
// 仍失败的留待下周定时任务自然重试（消息未软删，下次 ListSummarizedExpired 会再查到）
func (s *MessageTTLScheduler) softDeleteExpiredMessages(ctx context.Context) int64 {
	threshold := time.Now().UTC().Add(-s.ttl)
	var totalDeleted int64
	var failedBatches [][]uint // 收集失败批 ID 用于重试

	for {
		msgs, err := s.repos.Message.ListSummarizedExpired(threshold, s.batchSize)
		if err != nil {
			log.Printf("[msg-ttl-scheduler] 查询超期已压缩消息失败: %v", err)
			break
		}
		if len(msgs) == 0 {
			break
		}

		ids := make([]uint, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}

		n, err := s.repos.Message.SoftDeleteByIDs(ids)
		if err != nil {
			log.Printf("[msg-ttl-scheduler] 软删失败（本批 %d 条）: %v", len(ids), err)
			failedBatches = append(failedBatches, ids)
			continue
		}
		totalDeleted += n
		log.Printf("[msg-ttl-scheduler] 本批软删 %d 条（累计 %d）", n, totalDeleted)

		// 本批不足 batchSize，说明没有更多超期消息了
		if len(msgs) < s.batchSize {
			break
		}
		// 微停 100ms 让出 CPU，避免影响在线对话
		time.Sleep(100 * time.Millisecond)
	}

	// 整轮结束后重试失败批最多 3 次
	if len(failedBatches) > 0 {
		totalDeleted += s.retryFailedBatches(threshold, failedBatches)
	}
	return totalDeleted
}

// retryFailedBatches 重试失败批次，最多 3 次
// 重新查 ListSummarizedExpired 是为了确保消息仍未被软删（避免重复软删）
// 简化：重试时直接按 ids 重新调 SoftDeleteByIDs（GORM 软删对已软删记录是 no-op，不会重复计 RowsAffected）
func (s *MessageTTLScheduler) retryFailedBatches(threshold time.Time, failedBatches [][]uint) int64 {
	log.Printf("[msg-ttl-scheduler] 开始重试 %d 个失败批次", len(failedBatches))
	var totalDeleted int64
	for retry := 1; retry <= 3; retry++ {
		var stillFailed [][]uint
		for _, ids := range failedBatches {
			n, err := s.repos.Message.SoftDeleteByIDs(ids)
			if err != nil {
				stillFailed = append(stillFailed, ids)
				continue
			}
			totalDeleted += n
			log.Printf("[msg-ttl-scheduler] 重试第 %d 次：成功软删 %d 条", retry, n)
		}
		failedBatches = stillFailed
		if len(failedBatches) == 0 {
			log.Printf("[msg-ttl-scheduler] 重试第 %d 次后全部成功", retry)
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
		log.Printf("[msg-ttl-scheduler] 重试 3 次后仍有 %d 条软删失败，留待下周定时任务重试", totalFailed)
	}
	return totalDeleted
}
