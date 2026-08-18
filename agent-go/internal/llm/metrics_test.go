package llm

import (
	"testing"
	"time"
)

func mkMetric(ttft int64, total int64, hit, miss int, errMsg string) CallMetric {
	return CallMetric{
		Timestamp:        time.Now(),
		Model:            "deepseek-chat",
		TTFTMs:           ttft,
		TotalMs:          total,
		CacheHitTokens:   hit,
		CacheMissTokens:  miss,
		FinishReason:     "stop",
		Error:            errMsg,
	}
}

func TestMetricsRecordAndSnapshot(t *testing.T) {
	b := NewMetricsBuffer(16)

	// 空缓冲：summary 零值 + cache_hit_rate=-1
	s, recent := b.Snapshot(0)
	if s.SampleCount != 0 || s.CacheHitRate != -1 {
		t.Fatalf("空缓冲期望 SampleCount=0 CacheHitRate=-1, got %+v", s)
	}
	if len(recent) != 0 {
		t.Fatalf("空缓冲期望 recent 为空, got %d", len(recent))
	}

	// 3 条样本：TTFT 100/200/300，缓存 hit/miss = (90,10)/(50,50)/(0,100)
	b.Record(mkMetric(100, 1000, 90, 10, ""))
	b.Record(mkMetric(200, 2000, 50, 50, ""))
	b.Record(mkMetric(-1, 3000, 0, 100, "http_500")) // 错误样本：TTFT 未采样
	b.Record(mkMetric(150, 1500, 0, 0, ""))          // 端点未返回缓存字段样本

	s, recent = b.Snapshot(0)
	if s.SampleCount != 4 {
		t.Fatalf("SampleCount 期望 4, got %d", s.SampleCount)
	}
	if s.ErrorCount != 1 {
		t.Fatalf("ErrorCount 期望 1, got %d", s.ErrorCount)
	}
	// 加权命中率 = (90+50+0+0)/(90+50+0+0+10+50+100+0) = 140/300
	if s.CacheHitRate < 0.4666-1e-9 || s.CacheHitRate > 0.4667+1e-9 {
		t.Fatalf("CacheHitRate 期望 ~0.4667, got %f", s.CacheHitRate)
	}
	// TTFT 只统计成功样本（100/200/150）：mean=150, p95=200（升序[100,150,200] nearest-rank）
	if s.TTFTMeanMs != 150 {
		t.Fatalf("TTFTMeanMs 期望 150, got %f", s.TTFTMeanMs)
	}
	if s.TTFTP50Ms != 150 || s.TTFTP95Ms != 200 {
		t.Fatalf("TTFT P50/P95 期望 150/200, got %f/%f", s.TTFTP50Ms, s.TTFTP95Ms)
	}
	if s.TTFTMaxMs != 200 {
		t.Fatalf("TTFTMaxMs 期望 200, got %d", s.TTFTMaxMs)
	}
	// total mean = (1000+2000+3000+1500)/4
	if s.TotalMeanMs != 1875 {
		t.Fatalf("TotalMeanMs 期望 1875, got %f", s.TotalMeanMs)
	}
	// recent 新→旧
	if len(recent) != 4 || recent[0].TTFTMs != 150 || recent[3].TTFTMs != 100 {
		t.Fatalf("recent 期望新→旧排列, got %+v", recent)
	}
	// 单条命中率派生：90/(90+10)=0.9
	if recent[3].CacheHitRate < 0.89 || recent[3].CacheHitRate > 0.91 {
		t.Fatalf("单条 CacheHitRate 期望 0.9, got %f", recent[3].CacheHitRate)
	}
	// 全 miss 样本 → 0.0；hit+miss=0（端点未返回缓存字段）→ -1
	if recent[1].CacheHitRate != 0 {
		t.Fatalf("hit=0/miss=100 期望 CacheHitRate=0, got %f", recent[1].CacheHitRate)
	}
	if recent[0].CacheHitRate != -1 {
		t.Fatalf("hit+miss=0 期望 CacheHitRate=-1, got %f", recent[0].CacheHitRate)
	}
}

func TestMetricsRingBufferOverwrite(t *testing.T) {
	b := NewMetricsBuffer(3)
	for i := int64(1); i <= 5; i++ {
		b.Record(mkMetric(i*10, i*100, 0, 0, ""))
	}
	s, recent := b.Snapshot(0)
	if s.SampleCount != 3 {
		t.Fatalf("容量 3 写 5 条后期望 3, got %d", s.SampleCount)
	}
	// 保留最近 3 条（TTFT 30/40/50），最新在前
	if len(recent) != 3 || recent[0].TTFTMs != 50 || recent[2].TTFTMs != 30 {
		t.Fatalf("环形覆盖后期望保留 TTFT 50/40/30, got %+v", recent)
	}
}

func TestMetricsSnapshotLimit(t *testing.T) {
	b := NewMetricsBuffer(16)
	for i := int64(1); i <= 5; i++ {
		b.Record(mkMetric(i, i, 0, 0, ""))
	}
	_, recent := b.Snapshot(2)
	if len(recent) != 2 || recent[0].TTFTMs != 5 {
		t.Fatalf("limit=2 期望最新 2 条且首条 TTFT=5, got %+v", recent)
	}
	// summary 不受 limit 影响（仍统计全部缓冲样本）
	s, _ := b.Snapshot(2)
	if s.SampleCount != 5 {
		t.Fatalf("summary 期望统计全部 5 条, got %d", s.SampleCount)
	}
}

func TestPercentileSorted(t *testing.T) {
	cases := []struct {
		sorted []int64
		p      float64
		want   float64
	}{
		{[]int64{10, 20, 30, 40, 50}, 0.50, 30},
		{[]int64{10, 20, 30, 40, 50}, 0.95, 50},
		{[]int64{10, 20, 30, 40}, 0.50, 20}, // ceil(0.5*4)-1=1
		{[]int64{10}, 0.95, 10},
		{nil, 0.95, 0},
	}
	for _, c := range cases {
		if got := percentileSorted(c.sorted, c.p); got != c.want {
			t.Fatalf("percentileSorted(%v, %v) 期望 %v, got %v", c.sorted, c.p, c.want, got)
		}
	}
}
