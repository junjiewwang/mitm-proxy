package rule

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errsFor 解析内容并返回错误列表，便于逐条断言诊断信息。
func errsFor(t *testing.T, content string) []ParseError {
	t.Helper()
	p := &Parser{}
	_, errs := p.ParseContent(content)
	return errs
}

func TestParseFile(t *testing.T) {
	t.Run("解析文件并推导 ID", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "my-rules.rules")
		os.WriteFile(path, []byte(sampleRule), 0644)

		p := &Parser{}
		s, err := p.ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile: %v", err)
		}
		if s.ID != "my-rules" {
			t.Errorf("ID = %q, want my-rules (basename without extension)", s.ID)
		}
		if s.FilePath != path {
			t.Errorf("FilePath = %q, want %q", s.FilePath, path)
		}
		if s.Name != "Sample" {
			t.Errorf("Name = %q, want Sample", s.Name)
		}
		if s.UpdatedAt.IsZero() {
			t.Error("UpdatedAt should be stamped")
		}
	})

	t.Run("文件不存在返回错误", func(t *testing.T) {
		p := &Parser{}
		if _, err := p.ParseFile(filepath.Join(t.TempDir(), "nope.rules")); err == nil {
			t.Error("ParseFile should fail for a missing file")
		}
	})

	t.Run("语法错误记录在 ParseErrors 而非返回 error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "broken.rules")
		os.WriteFile(path, []byte("@name Broken\n/[unclosed/ http://x\n"), 0644)

		p := &Parser{}
		s, err := p.ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile should tolerate line-level errors, got %v", err)
		}
		if len(s.ParseErrors) == 0 {
			t.Error("the parse errors should be attached to the script")
		}
	})
}

func TestParseContentDirectiveErrors(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantMsg        string
		wantSuggestion string
		wantColumn     int
	}{
		{
			name:           "未知指令",
			content:        "@bogus something\n",
			wantMsg:        "unknown directive",
			wantSuggestion: "Valid directives: @name, @priority, @intercept, @config, @script, @timeout",
			wantColumn:     1,
		},
		{
			name:           "@priority 缺少值",
			content:        "@priority\n",
			wantMsg:        "@priority requires a value",
			wantSuggestion: "Example: @priority 100",
			wantColumn:     len("@priority") + 1,
		},
		{
			name:           "@priority 值非整数",
			content:        "@priority abc\n",
			wantMsg:        "@priority value must be integer",
			wantSuggestion: "Priority must be a positive integer, e.g. @priority 100",
			wantColumn:     len("@priority") + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := errsFor(t, tt.content)
			e := findError(errs, tt.wantMsg)
			if e == nil {
				t.Fatalf("got %+v, want an error containing %q", errs, tt.wantMsg)
			}
			if e.Suggestion != tt.wantSuggestion {
				t.Errorf("Suggestion = %q, want %q", e.Suggestion, tt.wantSuggestion)
			}
			if e.Column != tt.wantColumn {
				t.Errorf("Column = %d, want %d", e.Column, tt.wantColumn)
			}
			if e.Line != 1 {
				t.Errorf("Line = %d, want 1", e.Line)
			}
		})
	}
}

func TestParseContentRegexErrors(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantMsg        string
		wantSuggestion string
	}{
		{
			name:           "正则规则缺少目标",
			content:        "/^\\/api/\n",
			wantMsg:        "requires a target",
			wantSuggestion: "Add an http://, https://, or script:// target after the regex pattern",
		},
		{
			name:           "正则语法错误",
			content:        "/[unclosed/ http://backend\n",
			wantMsg:        "invalid regex",
			wantSuggestion: "Check regex syntax. Common issues: unescaped special chars, unmatched parentheses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := errsFor(t, tt.content)
			e := findError(errs, tt.wantMsg)
			if e == nil {
				t.Fatalf("got %+v, want an error containing %q", errs, tt.wantMsg)
			}
			if e.Suggestion != tt.wantSuggestion {
				t.Errorf("Suggestion = %q, want %q", e.Suggestion, tt.wantSuggestion)
			}
			if e.Column < 1 {
				t.Errorf("Column = %d, want a 1-based column", e.Column)
			}
			if e.Raw == "" {
				t.Error("Raw should carry the offending line for the UI to highlight")
			}
		})
	}
}

func TestComputeRegexErrorColumn(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		name    string
		rawLine string
		err     error
		want    int
	}{
		{"缺少目标且找不到 target", "/^\\/api/", errors.New("regex rule requires a target"), 1},
		{"分隔符不完整指向首个斜杠", "abc/def", errors.New("regex not properly delimited"), 4},
		{"分隔符不完整且无斜杠", "abcdef", errors.New("regex not properly delimited"), 1},
		{"空正则指向双斜杠之后", "// http://x", errors.New("empty regex pattern"), 2},
		{"空正则但无双斜杠", "nothing", errors.New("empty regex pattern"), 1},
		{"非法正则指向首个斜杠", "  /[bad/ http://x", errors.New("invalid regex: foo"), 3},
		{"非法正则且无斜杠", "bad", errors.New("invalid regex: foo"), 1},
		{"无法识别的错误回落到 1", "whatever", errors.New("some other problem"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.computeRegexErrorColumn(tt.rawLine, tt.err); got != tt.want {
				t.Errorf("computeRegexErrorColumn(%q) = %d, want %d", tt.rawLine, got, tt.want)
			}
		})
	}
}

func TestComputeRegexSuggestion(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		err  string
		want string
	}{
		{"regex rule requires a target", "Add an http://, https://, or script:// target after the regex pattern"},
		{"regex not properly delimited", "Wrap the regex pattern with slashes, e.g. /api/v1/"},
		{"empty regex pattern", "Add a regex pattern between the slashes, e.g. /api/v1/"},
		{"invalid regex: bad", "Check regex syntax. Common issues: unescaped special chars, unmatched parentheses"},
		{"unrecognized failure", ""},
	}
	for _, tt := range tests {
		if got := p.computeRegexSuggestion(errors.New(tt.err)); got != tt.want {
			t.Errorf("computeRegexSuggestion(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestComputeDirectiveErrorColumn(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		name    string
		rawLine string
		err     error
		want    int
	}{
		{"@priority 缺值", "@priority", errors.New("@priority requires a value"), 10},
		{"@priority 缺值带缩进", "  @priority", errors.New("@priority requires a value"), 12},
		{"@priority 非整数", "@priority abc", errors.New("@priority value must be integer"), 10},
		{"未知指令", "@bogus x", errors.New("unknown directive: @bogus"), 1},
		{"未知指令带缩进", "   @bogus x", errors.New("unknown directive: @bogus"), 4},
		{"错误不匹配任何分支", "@name x", errors.New("other"), 1},
		{"找不到指令文本", "no directive here", errors.New("@priority requires a value"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.computeDirectiveErrorColumn(tt.rawLine, tt.err); got != tt.want {
				t.Errorf("computeDirectiveErrorColumn(%q) = %d, want %d", tt.rawLine, got, tt.want)
			}
		})
	}
}

func TestComputeDirectiveSuggestion(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		err  string
		want string
	}{
		{"unknown directive: @x", "Valid directives: @name, @priority, @intercept, @config, @script, @timeout"},
		{"@priority requires a value", "Example: @priority 100"},
		{"@priority value must be integer", "Priority must be a positive integer, e.g. @priority 100"},
		{"something else", ""},
	}
	for _, tt := range tests {
		if got := p.computeDirectiveSuggestion(errors.New(tt.err)); got != tt.want {
			t.Errorf("computeDirectiveSuggestion(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestComputeHostsErrorColumn(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		name    string
		rawLine string
		err     error
		want    int
	}{
		{"缺少域名指向 IP 之后", "1.1.1.1", errors.New("hosts rule requires at least one domain"), 8},
		{"缺少目标指向路径之后", "example.com/api", errors.New("exact rule requires a target"), 16},
		{"格式非法回落到 1", "???", errors.New("invalid rule format"), 1},
		{"空行回落到 1", "", errors.New("hosts rule requires at least one domain"), 1},
		{"未识别的错误回落到 1", "1.1.1.1", errors.New("other"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.computeHostsErrorColumn(tt.rawLine, tt.err); got != tt.want {
				t.Errorf("computeHostsErrorColumn(%q) = %d, want %d", tt.rawLine, got, tt.want)
			}
		})
	}
}

func TestComputeHostsSuggestion(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		err  string
		want string
	}{
		{"hosts rule requires at least one domain", "Format: IP_ADDRESS domain1 domain2 ..."},
		{"exact rule requires a target", "Format: domain/path http://target-url"},
		{"invalid rule format", "Valid formats: /regex/ target, IP domain1 domain2, or domain/path target"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		if got := p.computeHostsSuggestion(errors.New(tt.err)); got != tt.want {
			t.Errorf("computeHostsSuggestion(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// TestIsIPLike 覆盖的是一个刻意宽松的启发式判断：
// "首字符是数字且含点" 或 "含冒号（IPv6）" 即视为 IP，
// 它只用于区分 hosts 行与 exact 行，不做 IP 合法性校验。
func TestIsIPLike(t *testing.T) {
	tests := []struct {
		in   string
		want bool
		why  string
	}{
		{"1.1.1.1", true, "标准 IPv4"},
		{"192.168.0.1", true, "标准 IPv4"},
		{"255.255.255.255", true, "标准 IPv4"},
		{"1.1.1", true, "段数不足也算 IP —— 启发式不校验段数"},
		{"1.1.1.1.1", true, "段数过多也算 IP —— 同上"},
		{"::1", true, "IPv6 靠冒号识别"},
		{"2001:db8::1", true, "IPv6"},
		{"example.com", false, "首字符非数字"},
		{"a.b.c.d", false, "首字符非数字"},
		{"localhost", false, "既无点也无冒号"},
		{"", false, "空字符串"},
		{"8host.com", true, "首字符是数字的域名会被误判为 IP（已知启发式局限）"},
	}
	for _, tt := range tests {
		if got := isIPLike(tt.in); got != tt.want {
			t.Errorf("isIPLike(%q) = %v, want %v (%s)", tt.in, got, tt.want, tt.why)
		}
	}
}

func TestFindTargetIndex(t *testing.T) {
	p := &Parser{}
	tests := []struct {
		name    string
		line    string
		wantPos bool // 是否应找到目标
	}{
		{"http 目标", "/^\\/api/ http://backend", true},
		{"https 目标", "/^\\/api/ https://backend", true},
		{"script 目标", "/^\\/api/ script://my-script", true},
		{"无目标", "/^\\/api/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.findTargetIndex(tt.line)
			if tt.wantPos && got < 0 {
				t.Errorf("findTargetIndex(%q) = %d, want a valid index", tt.line, got)
			}
			if !tt.wantPos && got >= 0 {
				t.Errorf("findTargetIndex(%q) = %d, want -1", tt.line, got)
			}
		})
	}
}

func TestParseContentHostsRule(t *testing.T) {
	p := &Parser{}
	s, errs := p.ParseContent("1.1.1.1 a.com b.com\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(s.Rules) != 1 {
		t.Fatalf("parsed %d rules, want 1", len(s.Rules))
	}
	r := s.Rules[0]
	if r.Type != RuleTypeHosts {
		t.Errorf("Type = %v, want hosts", r.Type)
	}
	if r.IP != "1.1.1.1" {
		t.Errorf("IP = %q, want 1.1.1.1", r.IP)
	}
	if len(r.Domains) != 2 || r.Domains[0] != "a.com" || r.Domains[1] != "b.com" {
		t.Errorf("Domains = %v, want [a.com b.com]", r.Domains)
	}
}

func TestParseContentCommentsAndBlankLines(t *testing.T) {
	content := "# a comment\n\n@name Test\n\n# another\n/^\\/api/ http://backend\n"
	p := &Parser{}
	s, errs := p.ParseContent(content)
	if len(errs) != 0 {
		t.Fatalf("comments and blank lines should not produce errors: %+v", errs)
	}
	if s.Name != "Test" {
		t.Errorf("Name = %q, want Test", s.Name)
	}
	if len(s.Rules) != 1 {
		t.Errorf("parsed %d rules, want 1", len(s.Rules))
	}
}

func TestParseContentDuplicateHostsDomain(t *testing.T) {
	// 同脚本内同域名映射到不同 IP，parser 应报重复并带上关联行号
	content := "1.1.1.1 dup.com\n2.2.2.2 dup.com\n"
	errs := errsFor(t, content)

	var dupErr *ParseError
	for i := range errs {
		if strings.Contains(strings.ToLower(errs[i].Message), "duplicate") {
			dupErr = &errs[i]
			break
		}
	}
	if dupErr == nil {
		t.Fatalf("got %+v, want a duplicate-domain diagnostic", errs)
	}
	if dupErr.RelatedLine != 1 {
		t.Errorf("RelatedLine = %d, want 1 (the first occurrence)", dupErr.RelatedLine)
	}
	if dupErr.RelatedText == "" {
		t.Error("RelatedText should carry the first occurrence's line for the UI")
	}
}

func TestParseContentOptionFlags(t *testing.T) {
	content := "/^\\/sse/ http://backend @stream @cors @timeout=5s\n"
	p := &Parser{}
	s, errs := p.ParseContent(content)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(s.Rules) != 1 {
		t.Fatalf("parsed %d rules, want 1", len(s.Rules))
	}
	r := s.Rules[0]
	if !r.Stream {
		t.Error("@stream should set Stream")
	}
	if !r.CORS {
		t.Error("@cors should set CORS")
	}
	// @timeout= 目前只被识别、不被解析（parser.go 标注为待完善），
	// 因此 Timeout 保持零值。若将来实现了解析，这条断言会失败并提示更新。
	if r.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 — @timeout parsing is not implemented yet; "+
			"if it now is, update this test and rule.Validator's timeout checks", r.Timeout)
	}
}
