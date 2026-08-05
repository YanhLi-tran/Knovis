package tools

import (
	"testing"
	"time"
)

// TestApprovalRegisterAndSubmit 注册审批 → 提交决定 → 验证 channel 接收
func TestApprovalRegisterAndSubmit(t *testing.T) {
	am := NewApprovalManager()
	approvalID := "test-approval-1"

	ch := am.Register(approvalID)

	// 提交批准决定
	go func() {
		time.Sleep(10 * time.Millisecond) // 模拟用户思考延迟
		ok := am.Submit(approvalID, ApprovalDecision{
			ApprovalID: approvalID,
			Approved:   true,
			Reason:     "测试批准",
		})
		if !ok {
			t.Error("Submit 应返回 true（审批存在）")
		}
	}()

	// 等待决定
	select {
	case decision := <-ch:
		if !decision.Approved {
			t.Fatal("应收到批准决定")
		}
		if decision.Reason != "测试批准" {
			t.Fatalf("Reason 应为 '测试批准'，实际 %s", decision.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待审批决定超时")
	}
}

// TestApprovalSubmitNonexistent 提交不存在的审批应返回 false
func TestApprovalSubmitNonexistent(t *testing.T) {
	am := NewApprovalManager()

	ok := am.Submit("nonexistent-id", ApprovalDecision{Approved: true})
	if ok {
		t.Fatal("提交不存在的审批应返回 false")
	}
}

// TestApprovalSubmitTwice 重复提交同一审批应返回 false（第二次）
func TestApprovalSubmitTwice(t *testing.T) {
	am := NewApprovalManager()
	approvalID := "test-approval-twice"

	ch := am.Register(approvalID)

	// 第一次提交（成功）
	ok1 := am.Submit(approvalID, ApprovalDecision{Approved: true})
	if !ok1 {
		t.Fatal("第一次 Submit 应返回 true")
	}

	// 接收决定（清空 channel）
	<-ch

	// 第二次提交（应失败，审批已被删除）
	ok2 := am.Submit(approvalID, ApprovalDecision{Approved: true})
	if ok2 {
		t.Fatal("第二次 Submit 应返回 false（审批已处理）")
	}
}

// TestApprovalReject 验证拒绝路径
func TestApprovalReject(t *testing.T) {
	am := NewApprovalManager()
	approvalID := "test-approval-reject"

	ch := am.Register(approvalID)

	go func() {
		time.Sleep(10 * time.Millisecond)
		am.Submit(approvalID, ApprovalDecision{
			ApprovalID: approvalID,
			Approved:   false,
			Reason:     "测试拒绝",
		})
	}()

	select {
	case decision := <-ch:
		if decision.Approved {
			t.Fatal("应收到拒绝决定（Approved=false）")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待审批决定超时")
	}
}

// TestApprovalConcurrent 并发注册不同审批 ID（互不影响）
func TestApprovalConcurrent(t *testing.T) {
	am := NewApprovalManager()

	// 并发注册 10 个审批
	ids := make([]string, 10)
	chs := make([]chan ApprovalDecision, 10)
	for i := 0; i < 10; i++ {
		ids[i] = "concurrent-approval-" + string(rune('A'+i))
		chs[i] = am.Register(ids[i])
	}

	// 并发提交所有决定
	for i := 0; i < 10; i++ {
		go func(idx int) {
			am.Submit(ids[idx], ApprovalDecision{Approved: true})
		}(i)
	}

	// 验证所有 channel 都收到决定
	for i := 0; i < 10; i++ {
		select {
		case <-chs[i]:
			// 成功收到
		case <-time.After(2 * time.Second):
			t.Fatalf("审批 %s 超时未收到决定", ids[i])
		}
	}
}
