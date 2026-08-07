package proxy

import "testing"

func TestMatchResultTypeName(t *testing.T) {
	cases := []struct {
		rt   RuleType
		want string
	}{
		{RuleTypeHosts, "hosts"},
		{RuleTypeProxy, "proxy"},
		{RuleTypePipeline, "pipeline"},
		{RuleTypeExact, "exact"},
		{RuleTypeScript, "script"},
		{RuleType(-1), "proxy"}, // 未知类型回退到 "proxy"
	}

	for _, c := range cases {
		m := &MatchResult{RuleType: c.rt}
		if got := m.TypeName(); got != c.want {
			t.Errorf("TypeName() for %d = %q, want %q", c.rt, got, c.want)
		}
	}
}
