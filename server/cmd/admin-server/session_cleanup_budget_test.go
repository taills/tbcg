package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type cleanupBudgetStub struct {
	mu        sync.Mutex
	calls     int
	completed chan int
	stopAfter int
	short     bool
	fail      bool
	cancel    context.CancelFunc
}

func (s *cleanupBudgetStub) CleanupExpiredSessions(ctx context.Context, limit int) (int64, error) {
	if _, ok := ctx.Deadline(); !ok || limit != 100 {
		return 0, errors.New("清理未使用时间或行数边界")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	if s.calls == s.stopAfter {
		s.completed <- s.calls
	}
	if s.fail {
		return 0, errors.New("测试清理失败")
	}
	if s.short {
		return 3, nil
	}
	return int64(limit), nil
}

func TestSessionCleanupDrainsSeveralBatchesWithinOneTick(t *testing.T) {
	stub := &cleanupBudgetStub{completed: make(chan int, 1), stopAfter: 10}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runSessionCleanup(ctx, stub, 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-stub.completed:
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("一个 tick 未在有界预算内排空多批积压 session")
	}
	cancel()
	<-done
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.calls != 10 {
		t.Fatalf("一个 tick 清理批数 = %d，预期固定上限 10", stub.calls)
	}
}

func TestSessionCleanupStopsOnShortBatchErrorOrCancellation(t *testing.T) {
	for _, check := range []struct {
		name  string
		short bool
		fail  bool
	}{
		{name: "short batch", short: true},
		{name: "database error", fail: true},
		{name: "cancel between batches"},
	} {
		t.Run(check.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stub := &cleanupBudgetStub{completed: make(chan int, 1), stopAfter: 1, short: check.short, fail: check.fail}
			if !check.short && !check.fail {
				stub.cancel = cancel
			}
			done := make(chan struct{})
			go func() {
				runSessionCleanup(ctx, stub, 100*time.Millisecond)
				close(done)
			}()
			select {
			case <-stub.completed:
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("有界 session 清理未执行")
			}
			cancel()
			<-done
			stub.mu.Lock()
			defer stub.mu.Unlock()
			if stub.calls != 1 {
				t.Fatalf("短批次、错误或取消后仍清理 %d 批，预期 1", stub.calls)
			}
		})
	}
}

func TestCleanupExpiredSessionBatchesReportsBoundedProgress(t *testing.T) {
	for _, check := range []struct {
		name         string
		short        bool
		fail         bool
		cancelDuring bool
		wantCount    int64
		wantCalls    int
		wantLimit    bool
		wantError    bool
	}{
		{name: "row budget", wantCount: 1000, wantCalls: 10, wantLimit: true},
		{name: "short batch", short: true, wantCount: 3, wantCalls: 1},
		{name: "failure", fail: true, wantCalls: 1, wantError: true},
		{name: "cancellation", cancelDuring: true, wantCount: 100, wantCalls: 1, wantError: true},
	} {
		t.Run(check.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			stub := &cleanupBudgetStub{short: check.short, fail: check.fail, stopAfter: 10, completed: make(chan int, 1)}
			if check.cancelDuring {
				stub.cancel = cancel
			}
			count, limited, err := cleanupExpiredSessionBatches(ctx, stub)
			if count != check.wantCount || limited != check.wantLimit || (err != nil) != check.wantError || stub.calls != check.wantCalls {
				t.Fatalf("清理结果 count/limited/calls/error=%d/%t/%d/%v，预期 %d/%t/%d/%t", count, limited, stub.calls, err, check.wantCount, check.wantLimit, check.wantCalls, check.wantError)
			}
		})
	}
}
