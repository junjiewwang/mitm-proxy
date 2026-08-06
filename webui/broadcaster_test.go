package webui

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewEventBroadcasterDefaults(t *testing.T) {
	if got := NewEventBroadcaster(0).size; got != 200 {
		t.Errorf("size = %d, want default 200", got)
	}
	if got := NewEventBroadcaster(-1).size; got != 200 {
		t.Errorf("size = %d, want default 200 for a negative input", got)
	}
	if got := NewEventBroadcaster(10).size; got != 10 {
		t.Errorf("size = %d, want 10", got)
	}
}

func TestBroadcasterSubscribeUnsubscribe(t *testing.T) {
	b := NewEventBroadcaster(10)
	if got := b.ClientCount(); got != 0 {
		t.Fatalf("ClientCount = %d, want 0", got)
	}

	ch1 := b.Subscribe()
	ch2 := b.Subscribe()
	if got := b.ClientCount(); got != 2 {
		t.Errorf("ClientCount = %d, want 2", got)
	}

	b.Unsubscribe(ch1)
	if got := b.ClientCount(); got != 1 {
		t.Errorf("ClientCount = %d, want 1", got)
	}

	// Unsubscribe 必须关闭 channel，否则 SSE 循环无法退出
	if _, open := <-ch1; open {
		t.Error("Unsubscribe should close the client channel")
	}

	b.Unsubscribe(ch2)
	if got := b.ClientCount(); got != 0 {
		t.Errorf("ClientCount = %d, want 0", got)
	}
}

func TestBroadcastDeliversToAllClients(t *testing.T) {
	b := NewEventBroadcaster(10)
	ch1 := b.Subscribe()
	ch2 := b.Subscribe()

	evt := &Event{URL: "http://example.com/api", Status: 200}
	b.Broadcast(evt)

	for i, ch := range []chan *Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got != evt {
				t.Errorf("client %d got %+v, want the broadcast event", i, got)
			}
		default:
			t.Errorf("client %d received nothing", i)
		}
	}
}

func TestBroadcastDropsWhenClientBufferIsFull(t *testing.T) {
	b := NewEventBroadcaster(500)
	ch := b.Subscribe() // 客户端 channel 容量为 64

	// 写入远超客户端缓冲的事件量：慢客户端应被丢弃事件而非阻塞广播
	for i := 0; i < 200; i++ {
		b.Broadcast(&Event{URL: fmt.Sprintf("http://example.com/%d", i)})
	}

	if got := len(ch); got != 64 {
		t.Errorf("client buffer holds %d events, want it capped at 64", got)
	}
	// 环形缓冲不受慢客户端影响，仍应记录全部事件
	if got := len(b.RecentEvents(1000)); got != 200 {
		t.Errorf("RecentEvents returned %d, want all 200 buffered events", got)
	}
}

func TestRecentEventsOrderAndLimit(t *testing.T) {
	b := NewEventBroadcaster(10)
	for i := 0; i < 5; i++ {
		b.Broadcast(&Event{URL: fmt.Sprintf("http://example.com/%d", i)})
	}

	t.Run("返回时间正序", func(t *testing.T) {
		events := b.RecentEvents(10)
		if len(events) != 5 {
			t.Fatalf("got %d events, want 5", len(events))
		}
		for i, evt := range events {
			want := fmt.Sprintf("http://example.com/%d", i)
			if evt.URL != want {
				t.Errorf("events[%d].URL = %q, want %q (chronological order)", i, evt.URL, want)
			}
		}
	})

	t.Run("limit 截取最新的若干条", func(t *testing.T) {
		events := b.RecentEvents(2)
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2", len(events))
		}
		// 保留最新的两条，且仍是正序
		if events[0].URL != "http://example.com/3" || events[1].URL != "http://example.com/4" {
			t.Errorf("got [%s %s], want the two most recent events", events[0].URL, events[1].URL)
		}
	})
}

func TestRecentEventsEmpty(t *testing.T) {
	b := NewEventBroadcaster(10)
	if got := b.RecentEvents(5); len(got) != 0 {
		t.Errorf("got %d events, want 0 on a fresh broadcaster", len(got))
	}
}

func TestRecentEventsRingBufferWrapAround(t *testing.T) {
	b := NewEventBroadcaster(3)
	for i := 0; i < 5; i++ {
		b.Broadcast(&Event{URL: fmt.Sprintf("http://example.com/%d", i)})
	}

	events := b.RecentEvents(10)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (the ring buffer size)", len(events))
	}
	// 最旧的 0 和 1 已被覆盖
	want := []string{"http://example.com/2", "http://example.com/3", "http://example.com/4"}
	for i, w := range want {
		if events[i].URL != w {
			t.Errorf("events[%d].URL = %q, want %q", i, events[i].URL, w)
		}
	}
}

func TestMatchStatusFilter(t *testing.T) {
	tests := []struct {
		status int
		filter string
		want   bool
	}{
		{200, "2xx", true},
		{299, "2xx", true},
		{300, "2xx", false},
		{199, "2xx", false},
		{301, "3xx", true},
		{404, "4xx", true},
		{500, "5xx", true},
		{599, "5xx", true},
		{600, "5xx", false},
		{404, "404", true},  // 具体状态码
		{404, "500", false},
		{404, "abc", true},  // 无法解析则不过滤
		{404, "", true},     // 空 filter 落到 default 分支，不过滤
	}
	for _, tt := range tests {
		if got := matchStatusFilter(tt.status, tt.filter); got != tt.want {
			t.Errorf("matchStatusFilter(%d, %q) = %v, want %v", tt.status, tt.filter, got, tt.want)
		}
	}
}

func TestRequestFilterMatch(t *testing.T) {
	evt := &Event{
		Type:     "match",
		Script:   "my-script",
		RuleType: "proxy",
		URL:      "http://example.com/API/Users",
		Status:   200,
	}

	tests := []struct {
		name   string
		filter RequestFilter
		want   bool
	}{
		{"空过滤器全通过", RequestFilter{}, true},
		{"type 命中", RequestFilter{Type: "match"}, true},
		{"type 不匹配", RequestFilter{Type: "forward"}, false},
		{"script 命中", RequestFilter{Script: "my-script"}, true},
		{"script 不匹配", RequestFilter{Script: "other"}, false},
		{"rule_type 命中", RequestFilter{RuleType: "proxy"}, true},
		{"rule_type 不匹配", RequestFilter{RuleType: "exact"}, false},
		{"URL 子串大小写不敏感", RequestFilter{URL: "api/users"}, true},
		{"URL 原样大小写", RequestFilter{URL: "API/Users"}, true},
		{"URL 不匹配", RequestFilter{URL: "nomatch"}, false},
		{"status 范围命中", RequestFilter{Status: "2xx"}, true},
		{"status 范围不匹配", RequestFilter{Status: "5xx"}, false},
		{"多条件同时命中", RequestFilter{Type: "match", Script: "my-script", Status: "2xx"}, true},
		{"多条件其一不匹配", RequestFilter{Type: "match", Script: "wrong"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.matchFilter(evt); got != tt.want {
				t.Errorf("matchFilter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequestFilterMatchNilEvent(t *testing.T) {
	f := &RequestFilter{}
	if f.matchFilter(nil) {
		t.Error("matchFilter(nil) should be false")
	}
}

// TestBroadcasterConcurrency 在 -race 下验证广播、订阅与读取可以并发进行。
func TestBroadcasterConcurrency(t *testing.T) {
	b := NewEventBroadcaster(100)

	var wg sync.WaitGroup
	done := make(chan struct{})

	// 持续消费，避免客户端 channel 填满后一直丢弃。
	// 不纳入 wg —— 它要等 wg 结束后才退出。
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		ch := b.Subscribe()
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()

	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Broadcast(&Event{URL: fmt.Sprintf("http://example.com/g%d/%d", g, i)})
			}
		}(g)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			b.RecentEvents(20)
			b.ClientCount()
			ch := b.Subscribe()
			b.Unsubscribe(ch)
		}
	}()

	wg.Wait()
	close(done)
	consumer.Wait()

	if got := len(b.RecentEvents(1000)); got != 100 {
		t.Errorf("RecentEvents returned %d, want the full ring buffer of 100", got)
	}
}
