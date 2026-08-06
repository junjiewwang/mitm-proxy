package rule

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// findError 返回第一条消息包含 substr 的错误，便于断言具体诊断。
func findError(errs []ParseError, substr string) *ParseError {
	for i := range errs {
		if strings.Contains(errs[i].Message, substr) {
			return &errs[i]
		}
	}
	return nil
}

func TestParseErrorError(t *testing.T) {
	e := &ParseError{Line: 42, Message: "something went wrong"}
	if got, want := e.Error(), "line 42: something went wrong"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNewValidator(t *testing.T) {
	v := NewValidator([]string{"adapter-a", "adapter-b"})
	if len(v.KnownAdapters) != 2 {
		t.Fatalf("KnownAdapters has %d entries, want 2", len(v.KnownAdapters))
	}
	if !v.KnownAdapters["adapter-a"] || !v.KnownAdapters["adapter-b"] {
		t.Errorf("KnownAdapters = %v, want both adapters present", v.KnownAdapters)
	}

	empty := NewValidator(nil)
	if len(empty.KnownAdapters) != 0 {
		t.Errorf("nil input should produce an empty adapter set, got %v", empty.KnownAdapters)
	}
}

func TestCheckRegexComplexity(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr string // 空表示应通过
	}{
		{"普通正则", `^/api/(\w+)$`, ""},
		{"嵌套量词 (a+)+", `(a+)+`, "nested quantifiers"},
		{"嵌套量词 (a*)*", `(a*)*`, "nested quantifiers"},
		{"嵌套量词多层括号", `((a+))+`, "nested quantifiers"},
		{"普通分组后跟量词不算嵌套", `(abc)+`, ""},
		{"量词在组内但组后无量词", `(a+)b`, ""},
		{"超长 pattern", "^/" + strings.Repeat("a", 510) + "$", "too long"},
		{"恰好 500 字符通过", strings.Repeat("a", 500), ""},
		{"501 字符超限", strings.Repeat("a", 501), "too long"},
		{"空 pattern", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRegexComplexity(tt.pattern)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("checkRegexComplexity() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRegexComplexity() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateProxyRule(t *testing.T) {
	v := NewValidator(nil)

	t.Run("合法规则无错误", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{
			Type: RuleTypeProxy, Line: 1,
			Pattern: regexp.MustCompile(`^/api$`), Target: "http://backend",
		})
		if len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("https 目标合法", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{Type: RuleTypeProxy, Line: 1, Target: "https://backend"})
		if len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("空目标报 error", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{Type: RuleTypeProxy, Line: 7, Target: ""})
		e := findError(errs, "target is empty")
		if e == nil {
			t.Fatalf("expected an empty-target error, got %+v", errs)
		}
		if e.Severity != "error" {
			t.Errorf("Severity = %q, want error", e.Severity)
		}
		if e.Line != 7 {
			t.Errorf("Line = %d, want 7", e.Line)
		}
	})

	t.Run("非 http scheme 报 error", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{Type: RuleTypeProxy, Line: 1, Target: "backend:8080"})
		e := findError(errs, "should start with http")
		if e == nil {
			t.Fatalf("expected a scheme error, got %+v", errs)
		}
		if e.Severity != "error" {
			t.Errorf("Severity = %q, want error", e.Severity)
		}
	})

	t.Run("嵌套量词报 warning", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{
			Type: RuleTypeProxy, Line: 1,
			Pattern: regexp.MustCompile(`(a+)+`), Target: "http://backend",
		})
		e := findError(errs, "regex complexity")
		if e == nil {
			t.Fatalf("expected a complexity warning, got %+v", errs)
		}
		if e.Severity != "warning" {
			t.Errorf("Severity = %q, want warning — complexity should not block loading", e.Severity)
		}
	})

	t.Run("超短超时报 warning", func(t *testing.T) {
		errs := v.validateProxyRule(&Rule{
			Type: RuleTypeProxy, Line: 1,
			Target: "http://backend", Timeout: 50 * time.Millisecond,
		})
		e := findError(errs, "timeout too short")
		if e == nil {
			t.Fatalf("expected a timeout warning, got %+v", errs)
		}
		if e.Severity != "warning" {
			t.Errorf("Severity = %q, want warning", e.Severity)
		}
	})

	t.Run("边界超时不报警", func(t *testing.T) {
		for _, d := range []time.Duration{0, 100 * time.Millisecond, time.Second} {
			errs := v.validateProxyRule(&Rule{
				Type: RuleTypeProxy, Line: 1, Target: "http://backend", Timeout: d,
			})
			if e := findError(errs, "timeout too short"); e != nil {
				t.Errorf("timeout %v should be accepted, got %q", d, e.Message)
			}
		}
	})
}

func TestValidatePipelineRule(t *testing.T) {
	t.Run("空适配器报 error 并短路", func(t *testing.T) {
		v := NewValidator([]string{"known"})
		errs := v.validatePipelineRule(&Rule{Type: RuleTypePipeline, Line: 3, Adapter: ""})
		if len(errs) != 1 {
			t.Fatalf("got %d errors, want exactly 1: %+v", len(errs), errs)
		}
		if errs[0].Severity != "error" {
			t.Errorf("Severity = %q, want error", errs[0].Severity)
		}
		if !strings.Contains(errs[0].Message, "adapter name is empty") {
			t.Errorf("Message = %q, want it to mention an empty adapter name", errs[0].Message)
		}
	})

	t.Run("已知适配器通过", func(t *testing.T) {
		v := NewValidator([]string{"known"})
		if errs := v.validatePipelineRule(&Rule{Type: RuleTypePipeline, Adapter: "known"}); len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("未知适配器报 warning", func(t *testing.T) {
		v := NewValidator([]string{"known"})
		errs := v.validatePipelineRule(&Rule{Type: RuleTypePipeline, Line: 3, Adapter: "mystery"})
		e := findError(errs, "unknown adapter")
		if e == nil {
			t.Fatalf("expected an unknown-adapter warning, got %+v", errs)
		}
		if e.Severity != "warning" {
			t.Errorf("Severity = %q, want warning", e.Severity)
		}
		if !strings.Contains(e.Message, "mystery") {
			t.Errorf("Message = %q, want it to name the offending adapter", e.Message)
		}
	})

	t.Run("无已知列表时跳过引用检查", func(t *testing.T) {
		v := NewValidator(nil)
		if errs := v.validatePipelineRule(&Rule{Type: RuleTypePipeline, Adapter: "anything"}); len(errs) != 0 {
			t.Errorf("with no known adapters the reference check should be skipped, got %+v", errs)
		}
	})
}

func TestValidateHostsRule(t *testing.T) {
	v := NewValidator(nil)

	t.Run("合法规则无错误", func(t *testing.T) {
		errs := v.validateHostsRule(&Rule{Type: RuleTypeHosts, IP: "1.1.1.1", Domains: []string{"a.com"}})
		if len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("空 IP 报 error", func(t *testing.T) {
		errs := v.validateHostsRule(&Rule{Type: RuleTypeHosts, Line: 2, Domains: []string{"a.com"}})
		if findError(errs, "IP is empty") == nil {
			t.Errorf("expected an empty-IP error, got %+v", errs)
		}
	})

	t.Run("空域名列表报 error", func(t *testing.T) {
		errs := v.validateHostsRule(&Rule{Type: RuleTypeHosts, Line: 2, IP: "1.1.1.1"})
		if findError(errs, "at least one domain") == nil {
			t.Errorf("expected a missing-domain error, got %+v", errs)
		}
	})

	t.Run("IP 与域名都缺失时报两条", func(t *testing.T) {
		errs := v.validateHostsRule(&Rule{Type: RuleTypeHosts, Line: 2})
		if len(errs) != 2 {
			t.Errorf("got %d errors, want 2 (both IP and domains missing): %+v", len(errs), errs)
		}
	})
}

func TestValidateExactRule(t *testing.T) {
	v := NewValidator(nil)

	if errs := v.validateExactRule(&Rule{Type: RuleTypeExact, Target: "http://backend"}); len(errs) != 0 {
		t.Errorf("a rule with a target should pass, got %+v", errs)
	}

	errs := v.validateExactRule(&Rule{Type: RuleTypeExact, Line: 5})
	e := findError(errs, "target is empty")
	if e == nil {
		t.Fatalf("expected an empty-target error, got %+v", errs)
	}
	if e.Severity != "error" || e.Line != 5 {
		t.Errorf("got (severity=%q, line=%d), want (error, 5)", e.Severity, e.Line)
	}
}

func TestValidateRuleDispatchesByType(t *testing.T) {
	v := NewValidator(nil)

	// 每种类型都给一条必然报错的规则，确认分发到了对应的校验器
	cases := []struct {
		name       string
		rule       Rule
		wantSubstr string
	}{
		{"proxy", Rule{Type: RuleTypeProxy, Target: ""}, "proxy rule target is empty"},
		{"pipeline", Rule{Type: RuleTypePipeline, Adapter: ""}, "pipeline rule adapter name is empty"},
		{"hosts", Rule{Type: RuleTypeHosts}, "hosts rule IP is empty"},
		{"exact", Rule{Type: RuleTypeExact, Target: ""}, "exact rule target is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := v.validateRule(&tc.rule)
			if findError(errs, tc.wantSubstr) == nil {
				t.Errorf("got %+v, want an error containing %q", errs, tc.wantSubstr)
			}
		})
	}

	t.Run("script 类型不走深度校验", func(t *testing.T) {
		if errs := v.validateRule(&Rule{Type: RuleTypeScript}); len(errs) != 0 {
			t.Errorf("script rules have no deep validation, got %+v", errs)
		}
	})
}

func TestValidateContent(t *testing.T) {
	v := NewValidator(nil)

	t.Run("合法内容无错误", func(t *testing.T) {
		content := "@name Test\n@intercept example.com\n/^\\/api/ http://backend\n"
		if errs := v.ValidateContent(content); len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("空内容无错误", func(t *testing.T) {
		if errs := v.ValidateContent(""); len(errs) != 0 {
			t.Errorf("got %d errors, want 0: %+v", len(errs), errs)
		}
	})

	t.Run("聚合语法层与规则层错误", func(t *testing.T) {
		// 嵌套量词能通过 parser，但会被 validateProxyRule 标记为复杂度警告，
		// 因此可验证 ValidateContent 确实把深度校验结果也聚合了进来。
		content := "@name Test\n/(a+)+/ http://backend\n"
		errs := v.ValidateContent(content)
		if findError(errs, "regex complexity") == nil {
			t.Errorf("expected the deep validation warning to be surfaced, got %+v", errs)
		}
	})

	t.Run("非法正则由 parser 检出", func(t *testing.T) {
		content := "@name Test\n/[unclosed/ http://backend\n"
		if errs := v.ValidateContent(content); len(errs) == 0 {
			t.Error("expected the parser to report the malformed regex")
		}
	})
}

func TestKnownAdapterList(t *testing.T) {
	v := NewValidator([]string{"a", "b", "c"})
	list := v.knownAdapterList()
	if len(list) != 3 {
		t.Fatalf("got %d adapters, want 3", len(list))
	}
	seen := map[string]bool{}
	for _, n := range list {
		seen[n] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("adapter %q missing from %v", want, list)
		}
	}

	if got := NewValidator(nil).knownAdapterList(); len(got) != 0 {
		t.Errorf("empty validator should return an empty list, got %v", got)
	}
}
