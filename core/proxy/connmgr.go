package proxy

import (
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

// ========================================
// ConnManager：有界连接管理器
// ========================================

// connSlot 连接槽位
type connSlot struct {
	id       string    // 连接标识
	conn     net.Conn  // 底层连接（可 close）
	lastUsed time.Time // 最后活跃时间
}

// ConnManager 管理 MITM 连接生命周期
// 核心机制：有界槽位 + LRU 淘汰
//   - 新连接写入活跃端 [0]
//   - 槽位满时从淘汰端踢出最久闲置的连接
//   - 连接有活动时提升到 [0]
type ConnManager struct {
	mu          sync.Mutex
	slots       []*connSlot       // 环形槽位，[0]=最活跃，[len-1]=待淘汰
	MaxConns    int               // 最大连接数
	idleTimeout time.Duration     // 闲置超时（超过此时间且槽位满时被踢）
	evicted     int64             // 统计：累计踢除数
}

// NewConnManager 创建连接管理器
func NewConnManager(maxConns int, idleTimeout time.Duration) *ConnManager {
	if maxConns <= 0 {
		maxConns = 100
	}
	if idleTimeout <= 0 {
		idleTimeout = 10 * time.Second
	}
	return &ConnManager{
		slots:       make([]*connSlot, 0, maxConns),
		MaxConns:    maxConns,
		idleTimeout: idleTimeout,
	}
}

// Stats 返回连接统计
func (m *ConnManager) Stats() (active, evicted int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.slots)), m.evicted
}

// Register 注册新连接。如果槽位满，踢除最旧闲置连接。
// 返回 true 表示注册成功，false 表示槽位满且无闲置可踢（应拒绝连接）。
func (m *ConnManager) Register(id string, conn net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// 槽位未满 → 直接插入活跃端
	if len(m.slots) < m.MaxConns {
		m.slots = append([]*connSlot{{id: id, conn: conn, lastUsed: now}}, m.slots...)
		log.Printf("[ConnMgr] +%s (active=%d/%d)", id, len(m.slots), m.MaxConns)
		return true
	}

	// 槽位满 → 尝试踢除闲置连接
	evictedIdx := m.findEvictable(now)
	if evictedIdx < 0 {
		return false // 全部活跃，拒绝
	}

	// 踢除并替换
	old := m.slots[evictedIdx]
	old.conn.Close()
	m.evicted++

	// 删除旧槽位，插入新槽位到头部
	m.slots = append(m.slots[:evictedIdx], m.slots[evictedIdx+1:]...)
	m.slots = append([]*connSlot{{id: id, conn: conn, lastUsed: now}}, m.slots...)

	log.Printf("[ConnMgr] 🔄 +%s (evicted stale %s, idle=%.1fs, active=%d/%d, total_evicted=%d)",
		id, old.id, now.Sub(old.lastUsed).Seconds(), len(m.slots), m.MaxConns, m.evicted)
	return true
}

// Touch 记录连接有活动（LRU 提升到活跃端）
func (m *ConnManager) Touch(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, s := range m.slots {
		if s.id == id {
			s.lastUsed = time.Now()
			if i > 0 {
				// 移到头部
				slot := m.slots[i]
				copy(m.slots[1:i+1], m.slots[0:i])
				m.slots[0] = slot
			}
			return
		}
	}
}

// Unregister 注销连接
func (m *ConnManager) Unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, s := range m.slots {
		if s.id == id {
			m.slots = append(m.slots[:i], m.slots[i+1:]...)
			log.Printf("[ConnMgr] -%s (active=%d/%d)", id, len(m.slots), m.MaxConns)
			return
		}
	}
}

// findEvictable 从淘汰端（尾部）扫描，返回第一个闲置连接的索引
func (m *ConnManager) findEvictable(now time.Time) int {
	// 从尾部（最旧）向前扫描
	ordered := make([]struct {
		idx      int
		idleTime time.Duration
	}, len(m.slots))

	for i, s := range m.slots {
		ordered[i].idx = i
		ordered[i].idleTime = now.Sub(s.lastUsed)
	}

	// 按闲置时间降序排列（最旧的在前）
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].idleTime > ordered[j].idleTime
	})

	if ordered[0].idleTime > m.idleTimeout {
		return ordered[0].idx
	}
	return -1
}
