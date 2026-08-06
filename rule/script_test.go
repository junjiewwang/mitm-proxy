package rule

import (
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestScriptStatsAdoptFrom(t *testing.T) {
	src := &ScriptStats{
		TotalHits: 42,
		LastHitAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
		RuleHits:  map[int]int64{3: 10, 7: 32},
	}
	dst := &ScriptStats{}

	dst.AdoptFrom(src)

	if got := dst.Hits(); got != 42 {
		t.Errorf("TotalHits = %d, want 42", got)
	}
	if !dst.LastHitAt.Equal(src.LastHitAt) {
		t.Errorf("LastHitAt = %v, want %v", dst.LastHitAt, src.LastHitAt)
	}
	if dst.RuleHits[3] != 10 || dst.RuleHits[7] != 32 {
		t.Errorf("RuleHits = %v, want map[3:10 7:32]", dst.RuleHits)
	}
}

func TestScriptStatsAdoptFromDeepCopiesRuleHits(t *testing.T) {
	src := &ScriptStats{TotalHits: 5, RuleHits: map[int]int64{1: 5}}
	dst := &ScriptStats{}
	dst.AdoptFrom(src)

	// 接管后两者不应再共享底层 map，否则旧脚本的后续命中会串写到新脚本
	dst.RuleHits[1] = 999
	dst.RuleHits[2] = 111

	if src.RuleHits[1] != 5 {
		t.Errorf("src.RuleHits[1] = %d, want 5 — the map must be deep-copied", src.RuleHits[1])
	}
	if _, exists := src.RuleHits[2]; exists {
		t.Error("writing to dst leaked a new key into src")
	}
}

func TestScriptStatsAdoptFromEdgeCases(t *testing.T) {
	t.Run("nil 源不 panic 且不改变目标", func(t *testing.T) {
		dst := &ScriptStats{TotalHits: 7, RuleHits: map[int]int64{1: 7}}
		dst.AdoptFrom(nil)
		if dst.Hits() != 7 || dst.RuleHits[1] != 7 {
			t.Errorf("AdoptFrom(nil) should be a no-op, got hits=%d", dst.Hits())
		}
	})

	t.Run("自我接管是空操作", func(t *testing.T) {
		s := &ScriptStats{TotalHits: 3, RuleHits: map[int]int64{1: 3}}
		s.AdoptFrom(s) // 若未短路会自死锁
		if s.Hits() != 3 || s.RuleHits[1] != 3 {
			t.Errorf("self-adopt should be a no-op, got hits=%d", s.Hits())
		}
	})

	t.Run("源为空统计", func(t *testing.T) {
		dst := &ScriptStats{TotalHits: 9, RuleHits: map[int]int64{1: 9}}
		dst.AdoptFrom(&ScriptStats{})
		if dst.Hits() != 0 {
			t.Errorf("TotalHits = %d, want 0 after adopting empty stats", dst.Hits())
		}
		if len(dst.RuleHits) != 0 {
			t.Errorf("RuleHits = %v, want empty", dst.RuleHits)
		}
		// RuleHits 必须是可写的空 map 而非 nil，避免调用方再判空
		dst.RuleHits[1] = 1
	})

	t.Run("源 RuleHits 为 nil", func(t *testing.T) {
		dst := &ScriptStats{}
		dst.AdoptFrom(&ScriptStats{TotalHits: 4, RuleHits: nil})
		if dst.Hits() != 4 {
			t.Errorf("TotalHits = %d, want 4", dst.Hits())
		}
		if dst.RuleHits == nil {
			t.Error("RuleHits should be a usable empty map, not nil")
		}
	})
}

// TestScriptStatsAdoptFromUnderConcurrentMatch 复现 copylocks 修复要防的场景：
// 热加载接管统计的同时仍有请求在通过 Match 递增计数。
// 旧写法 script.Stats = old.Stats 会连 mutex 一起拷贝，在 -race 下暴露为竞争。
func TestScriptStatsAdoptFromUnderConcurrentMatch(t *testing.T) {
	old := &RuleScript{
		ID: "s", Enabled: true,
		Rules: []Rule{{
			Type: RuleTypeProxy, Line: 1,
			Pattern: regexp.MustCompile(`^/api`), Target: "http://backend",
		}},
	}
	e := NewRuleEngine()
	e.Compile([]*RuleScript{old})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 持续制造命中，模拟在线流量
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := mustReq(t, "http://example.com/api/x")
			for {
				select {
				case <-stop:
					return
				default:
					e.Match(req)
					old.Stats.Hits() // 读侧同样必须走原子访问
				}
			}
		}()
	}

	// 并发地反复接管统计，模拟热加载
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			fresh := &RuleScript{ID: "s", Enabled: true, Rules: old.Rules}
			fresh.Stats.AdoptFrom(&old.Stats)
			if fresh.Stats.Hits() < 0 {
				t.Error("adopted hit count should never be negative")
			}
		}
		close(stop)
	}()

	wg.Wait()
}

func TestScriptStatsHits(t *testing.T) {
	s := &ScriptStats{}
	if got := s.Hits(); got != 0 {
		t.Errorf("Hits() = %d, want 0 on a zero-valued stats", got)
	}

	s.TotalHits = 17
	if got := s.Hits(); got != 17 {
		t.Errorf("Hits() = %d, want 17", got)
	}
}
