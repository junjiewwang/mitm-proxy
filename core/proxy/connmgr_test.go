package proxy

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConn 仅实现 Close，用于观察 ConnManager 的淘汰行为。
// 其余 net.Conn 方法不会被 ConnManager 调用。
type fakeConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *fakeConn) Close() error {
	c.closed.Store(true)
	return nil
}

// slotIDs 按活跃顺序返回槽位 ID（[0] 最活跃），仅供测试断言 LRU 顺序。
func slotIDs(m *ConnManager) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, len(m.slots))
	for i, s := range m.slots {
		ids[i] = s.id
	}
	return ids
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestNewConnManagerDefaults(t *testing.T) {
	m := NewConnManager(0, 0)
	if m.MaxConns != 100 {
		t.Errorf("MaxConns = %d, want default 100", m.MaxConns)
	}
	if m.idleTimeout != 10*time.Second {
		t.Errorf("idleTimeout = %v, want default 10s", m.idleTimeout)
	}

	m2 := NewConnManager(-5, -time.Second)
	if m2.MaxConns != 100 || m2.idleTimeout != 10*time.Second {
		t.Error("negative values should fall back to defaults")
	}

	m3 := NewConnManager(8, 3*time.Second)
	if m3.MaxConns != 8 || m3.idleTimeout != 3*time.Second {
		t.Error("explicit values should be preserved")
	}
}

func TestConnManagerRegisterPrependsToActiveEnd(t *testing.T) {
	m := NewConnManager(4, time.Hour)

	for _, id := range []string{"a", "b", "c"} {
		if !m.Register(id, &fakeConn{}) {
			t.Fatalf("Register(%q) = false, want true (slots not full)", id)
		}
	}

	// 新连接插入活跃端，因此顺序与注册顺序相反
	if got := slotIDs(m); !equalIDs(got, []string{"c", "b", "a"}) {
		t.Errorf("slots = %v, want [c b a]", got)
	}

	active, evicted := m.Stats()
	if active != 3 {
		t.Errorf("active = %d, want 3", active)
	}
	if evicted != 0 {
		t.Errorf("evicted = %d, want 0", evicted)
	}
}

func TestConnManagerRegisterRejectsWhenAllActive(t *testing.T) {
	// idleTimeout 很长 → 没有连接会被判定为闲置
	m := NewConnManager(2, time.Hour)
	m.Register("a", &fakeConn{})
	m.Register("b", &fakeConn{})

	c := &fakeConn{}
	if m.Register("c", c) {
		t.Error("Register should return false when slots are full and nothing is evictable")
	}
	if c.closed.Load() {
		t.Error("rejected connection must not be closed by the manager")
	}
	if got := slotIDs(m); !equalIDs(got, []string{"b", "a"}) {
		t.Errorf("slots = %v, want [b a] unchanged after rejection", got)
	}
}

func TestConnManagerRegisterEvictsMostIdle(t *testing.T) {
	m := NewConnManager(2, 20*time.Millisecond)

	oldest := &fakeConn{}
	m.Register("a", oldest)
	time.Sleep(30 * time.Millisecond) // 让 a 明显比 b 更闲置
	m.Register("b", &fakeConn{})
	time.Sleep(30 * time.Millisecond) // 两者都超过 idleTimeout，a 仍是最闲置的

	if !m.Register("c", &fakeConn{}) {
		t.Fatal("Register should succeed by evicting the most idle connection")
	}
	if !oldest.closed.Load() {
		t.Error("evicted connection should have been closed")
	}
	// 淘汰 a 后，c 插入活跃端
	if got := slotIDs(m); !equalIDs(got, []string{"c", "b"}) {
		t.Errorf("slots = %v, want [c b]", got)
	}

	active, evicted := m.Stats()
	if active != 2 {
		t.Errorf("active = %d, want 2", active)
	}
	if evicted != 1 {
		t.Errorf("evicted = %d, want 1", evicted)
	}
}

func TestConnManagerTouchPromotesToFront(t *testing.T) {
	m := NewConnManager(4, time.Hour)
	m.Register("a", &fakeConn{})
	m.Register("b", &fakeConn{})
	m.Register("c", &fakeConn{})
	// 当前顺序 [c b a]

	m.Touch("a")
	if got := slotIDs(m); !equalIDs(got, []string{"a", "c", "b"}) {
		t.Errorf("after Touch(a) slots = %v, want [a c b]", got)
	}

	// Touch 已在头部的连接应保持顺序不变
	m.Touch("a")
	if got := slotIDs(m); !equalIDs(got, []string{"a", "c", "b"}) {
		t.Errorf("after re-Touch(a) slots = %v, want [a c b] unchanged", got)
	}

	// 中间元素提升，其余保持相对顺序
	m.Touch("c")
	if got := slotIDs(m); !equalIDs(got, []string{"c", "a", "b"}) {
		t.Errorf("after Touch(c) slots = %v, want [c a b]", got)
	}
}

func TestConnManagerTouchUnknownIDIsNoop(t *testing.T) {
	m := NewConnManager(4, time.Hour)
	m.Register("a", &fakeConn{})
	m.Register("b", &fakeConn{})

	m.Touch("does-not-exist")

	if got := slotIDs(m); !equalIDs(got, []string{"b", "a"}) {
		t.Errorf("slots = %v, want [b a] unchanged", got)
	}
}

func TestConnManagerTouchRefreshesIdleTime(t *testing.T) {
	m := NewConnManager(2, 30*time.Millisecond)
	a := &fakeConn{}
	b := &fakeConn{}
	m.Register("a", a)
	m.Register("b", b)

	time.Sleep(40 * time.Millisecond)
	m.Touch("a") // a 重新变活跃，b 成为最闲置的

	if !m.Register("c", &fakeConn{}) {
		t.Fatal("Register should succeed, b is idle beyond the timeout")
	}
	if a.closed.Load() {
		t.Error("touched connection a should not be evicted")
	}
	if !b.closed.Load() {
		t.Error("idle connection b should have been evicted")
	}
}

func TestConnManagerUnregister(t *testing.T) {
	m := NewConnManager(4, time.Hour)
	m.Register("a", &fakeConn{})
	m.Register("b", &fakeConn{})
	m.Register("c", &fakeConn{})

	m.Unregister("b")
	if got := slotIDs(m); !equalIDs(got, []string{"c", "a"}) {
		t.Errorf("slots = %v, want [c a]", got)
	}

	// 注销不存在的 ID 不应影响现有槽位
	m.Unregister("zzz")
	if got := slotIDs(m); !equalIDs(got, []string{"c", "a"}) {
		t.Errorf("slots = %v, want [c a] unchanged", got)
	}

	if active, _ := m.Stats(); active != 2 {
		t.Errorf("active = %d, want 2", active)
	}

	// 注销后腾出的槽位可以再次注册
	if !m.Register("d", &fakeConn{}) {
		t.Error("Register should succeed after a slot was freed")
	}
}

// TestConnManagerConcurrentAccess 在 -race 下验证槽位切片的原地搬移不会撕裂。
func TestConnManagerConcurrentAccess(t *testing.T) {
	m := NewConnManager(16, time.Millisecond)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				id := fmt.Sprintf("g%d-%d", g, i)
				m.Register(id, &fakeConn{})
				m.Touch(id)
				m.Stats()
				m.Unregister(id)
			}
		}(g)
	}
	wg.Wait()

	if active, _ := m.Stats(); active != 0 {
		t.Errorf("active = %d, want 0 after every connection was unregistered", active)
	}
}
