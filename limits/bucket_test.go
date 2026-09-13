package limits

import (
	"sync"
	"testing"
	"time"
)

// fakeClock 是可推进的单调时钟，用于让桶的补充行为完全确定。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestBucketStartsFullAndRefills(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(2, 4, clock.Now) // 2 token/s，容量 4

	if level := bucket.level(); level != 4 {
		t.Fatalf("初始水位 = %v, want 4", level)
	}
	if ok, _ := bucket.tryTake(4); !ok {
		t.Fatal("容量内的首次取用应当成功")
	}
	if level := bucket.level(); level != 0 {
		t.Fatalf("取空后水位 = %v, want 0", level)
	}
	if ok, wait := bucket.tryTake(1); ok {
		t.Fatal("空桶不应立即满足")
	} else if wait != 500*time.Millisecond {
		t.Fatalf("等待时间 = %v, want 500ms", wait)
	}

	clock.Advance(500 * time.Millisecond)
	if ok, _ := bucket.tryTake(1); !ok {
		t.Fatal("补充 500ms 后应当满足 1 个 token")
	}

	clock.Advance(time.Hour)
	if level := bucket.level(); level != 4 {
		t.Fatalf("长时间补充后应封顶在容量 4，实际 %v", level)
	}
}

func TestBucketUnlimited(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(0, 0, clock.Now)

	if !bucket.unlimited() {
		t.Fatal("速率 0 应当是不限")
	}
	if ok, wait := bucket.tryTake(1_000_000); !ok || wait != 0 {
		t.Fatalf("不限桶必须立即满足，got ok=%v wait=%v", ok, wait)
	}
	if level := bucket.level(); level != 0 {
		t.Fatalf("不限桶水位 = %v", level)
	}
}

func TestBucketZeroAmountAlwaysAllowed(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(1, 1, clock.Now)
	if ok, _ := bucket.tryTake(0); !ok {
		t.Fatal("零消耗必须允许")
	}
	if ok, _ := bucket.tryTake(-5); !ok {
		t.Fatal("负消耗必须视为零")
	}
	if level := bucket.level(); level != 1 {
		t.Fatalf("零消耗不应改变水位，实际 %v", level)
	}
}

func TestBucketOversizedRequestBecomesDebt(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(1, 2, clock.Now) // 容量 2

	// 单次请求 10 个 token > 容量：不能永久排队。
	ok, wait := bucket.tryTake(10)
	if !ok || wait != 0 {
		t.Fatalf("超容量请求应当被吸收而不是等待，got ok=%v wait=%v", ok, wait)
	}
	if raised := bucket.raiseCount(); raised != 1 {
		t.Fatalf("超容量计数 = %d, want 1", raised)
	}
	if level := bucket.level(); level != -8 {
		t.Fatalf("透支后水位 = %v, want -8", level)
	}

	// 透支必须真的还：补 1s 只回 1 个 token，仍欠 7 个。
	clock.Advance(time.Second)
	if ok, _ := bucket.tryTake(1); ok {
		t.Fatal("透支未偿还完之前不应满足新请求")
	}
	clock.Advance(9 * time.Second)
	if ok, _ := bucket.tryTake(2); !ok {
		t.Fatal("偿还完毕后应恢复供给")
	}
}

func TestBucketSetClampsLevel(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(10, 100, clock.Now)

	bucket.set(10, 5)
	if level := bucket.level(); level != 5 {
		t.Fatalf("缩容后水位应被夹到新容量，实际 %v", level)
	}

	// 调低速率：不再新增额度，容量内的取用仍成立，超出部分必须等待。
	clock.Advance(time.Second)
	bucket.set(0.5, 5)
	if rate := bucket.ratePerSecond(); rate != 0.5 {
		t.Fatalf("rate = %v, want 0.5", rate)
	}
	if ok, _ := bucket.tryTake(5); !ok {
		t.Fatal("容量内的取用应当成功")
	}
	if ok, wait := bucket.tryTake(1); ok || wait != 2*time.Second {
		t.Fatalf("低速率下超量取用应等待 2s，got ok=%v wait=%v", ok, wait)
	}
}

func TestBucketGiveAndCharge(t *testing.T) {
	clock := newFakeClock()
	bucket := newBucket(1, 4, clock.Now)

	if ok, _ := bucket.tryTake(4); !ok {
		t.Fatal("take failed")
	}
	bucket.give(2)
	if level := bucket.level(); level != 2 {
		t.Fatalf("give 后水位 = %v, want 2", level)
	}
	bucket.give(100)
	if level := bucket.level(); level != 4 {
		t.Fatalf("give 不应超过容量，实际 %v", level)
	}

	if debt := bucket.charge(1); debt != 0 {
		t.Fatalf("额度内 charge 不应产生债务，debt=%v", debt)
	}
	if debt := bucket.charge(10); debt != 7 {
		t.Fatalf("debt = %v, want 7", debt)
	}
	if level := bucket.level(); level != 0 {
		t.Fatalf("charge 后不应为负，实际 %v", level)
	}
}
