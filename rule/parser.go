package rule

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"mitm-proxy/core/security"
)

// Parser 规则文件解析器（Whistle 兼容扩展语法）
type Parser struct{}

// ParseFile 解析规则文件，返回 RuleScript
func (p *Parser) ParseFile(path string) (*RuleScript, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rule file: %w", err)
	}

	script, parseErrors := p.ParseContent(string(content))
	script.FilePath = path
	script.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	script.UpdatedAt = time.Now()

	script.ParseErrors = parseErrors

	return script, nil
}

// ParseContent 解析规则内容字符串（供 Web UI 实时校验使用）
func (p *Parser) ParseContent(content string) (*RuleScript, []ParseError) {
	script := &RuleScript{
		Enabled: true,
		Scripts: make(map[string]string),
	}
	var errors []ParseError

	scanner := bufio.NewScanner(strings.NewReader(content))
	// 支持超长行（JS 脚本可能单行很长）
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)

		// 空行
		if line == "" {
			continue
		}

		// 注释
		if strings.HasPrefix(line, "#") {
			continue
		}

		// 多行块指令（@config / @script）
		if strings.HasPrefix(line, "@config") {
			configBlock, endLine, err := p.readBlock(scanner, &lineNo, "config")
			if err != nil {
				errors = append(errors, ParseError{
					Line:       lineNo,
					Column:     len(rawLine) + 1,
					Severity:   "error",
					Message:    err.Error(),
					Raw:        line,
					Suggestion: "Add @end to close the config block",
				})
				continue
			}
			if err := p.parseConfigBlock(script, configBlock); err != nil {
				errors = append(errors, ParseError{Line: lineNo, Column: 1, Severity: "error", Message: err.Error(), Raw: line})
			}
			lineNo = endLine // 跳过多行块
			continue
		}

		if strings.HasPrefix(line, "@script") {
			scriptName, isExternal, externalPath := p.parseScriptHeader(line)
			if isExternal {
				// @script name file://path — 引用外部 JS 文件
				// 路径遍历防护：拒绝含 ../ 的路径
				if _, err := security.SanitizePath("", externalPath); err != nil {
					errors = append(errors, ParseError{Line: lineNo, Column: 1, Severity: "error", Message: fmt.Sprintf("path traversal blocked: %v", err), Raw: line})
					continue
				}
				extSource, err := os.ReadFile(externalPath)
				if err != nil {
					errors = append(errors, ParseError{Line: lineNo, Column: 1, Severity: "error", Message: fmt.Sprintf("read external script: %v", err), Raw: line})
				} else {
					if script.Scripts == nil {
						script.Scripts = make(map[string]string)
					}
					script.Scripts[scriptName] = string(extSource)
				}
				continue
			}

			scriptBlock, endLine, err := p.readBlock(scanner, &lineNo, "script")
			if err != nil {
				errors = append(errors, ParseError{
					Line:       lineNo,
					Column:     len(rawLine) + 1,
					Severity:   "error",
					Message:    err.Error(),
					Raw:        line,
					Suggestion: "Add @end to close the script block",
				})
				continue
			}
			if script.Scripts == nil {
				script.Scripts = make(map[string]string)
			}
			script.Scripts[scriptName] = scriptBlock
			_ = endLine
			continue
		}

		// 单独的 @end（无开头匹配的多行块，忽略）
		if line == "@end" {
			continue
		}

		// 指令（@开头）
		if strings.HasPrefix(line, "@") {
			if err := p.parseDirective(script, line, lineNo, rawLine); err != nil {
				col := p.computeDirectiveErrorColumn(rawLine, err)
				sug := p.computeDirectiveSuggestion(err)
				errors = append(errors, ParseError{Line: lineNo, Column: col, Severity: "error", Message: err.Error(), Raw: line, Suggestion: sug})
			}
			continue
		}

		// 正则规则（/开头）
		if strings.HasPrefix(line, "/") {
			rule, err := p.parseRegexRule(line, lineNo, rawLine)
			if err != nil {
				col := p.computeRegexErrorColumn(rawLine, err)
				sug := p.computeRegexSuggestion(err)
				errors = append(errors, ParseError{Line: lineNo, Column: col, Severity: "error", Message: err.Error(), Raw: line, Suggestion: sug})
				continue
			}
			script.Rules = append(script.Rules, *rule)
			continue
		}

		// hosts 映射（IP 开头）或精确路径
		rule, err := p.parseHostsOrExact(line, lineNo, rawLine)
		if err != nil {
			col := p.computeHostsErrorColumn(rawLine, err)
			sug := p.computeHostsSuggestion(err)
			errors = append(errors, ParseError{Line: lineNo, Column: col, Severity: "error", Message: err.Error(), Raw: line, Suggestion: sug})
			continue
		}
		script.Rules = append(script.Rules, *rule)
	}

	return script, errors
}

// readBlock 读取直到 @end 的多行内容块
// scanner: 已经定位在 @config/@script 行的下一行
// lineNo: 当前行号指针
// blockType: 用于错误消息
// 返回: 块内容（去掉首尾空白）, @end 所在行号, error
func (p *Parser) readBlock(scanner *bufio.Scanner, lineNo *int, blockType string) (string, int, error) {
	var lines []string
	startLine := *lineNo

	for scanner.Scan() {
		*lineNo++
		rawLine := scanner.Text()
		trimmed := strings.TrimSpace(rawLine)

		if trimmed == "@end" {
			return strings.Join(lines, "\n"), *lineNo, nil
		}

		// 保留原始缩进（JS/YAML 缩进敏感）
		lines = append(lines, rawLine)
	}

	return "", *lineNo, fmt.Errorf("%s block opened at line %d but never closed (missing @end)", blockType, startLine)
}

// parseConfigBlock 解析 @config...@end 中的 YAML 内容到 script.Config
func (p *Parser) parseConfigBlock(script *RuleScript, yamlContent string) error {
	if yamlContent == "" {
		script.Config = make(map[string]any)
		return nil
	}

	// yaml.v3 将整数键的嵌套 map 解析为 map[interface{}]interface{}，
	// 注入 goja VM 时 goja 将其转为 JS 的 number 键，但脚本使用 String() 做字符串键查询会 miss。
	// 先解到 interface{} 再递归转换为 map[string]interface{} 确保所有键为 string。
	var raw interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		return fmt.Errorf("parse @config YAML: %w", err)
	}
	script.Config = normalizeYAMLMap(raw).(map[string]interface{})
	return nil
}

// normalizeYAMLMap 递归将 yaml.v3 的 map[interface{}]interface{} 转为 map[string]interface{}
func normalizeYAMLMap(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		// 顶级 map 已正确，但嵌套可能仍有 map[interface{}]interface{}
		for k, vv := range val {
			val[k] = normalizeYAMLMap(vv)
		}
		return val
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(val))
		for k, vv := range val {
			m[fmt.Sprint(k)] = normalizeYAMLMap(vv)
		}
		return m
	case []interface{}:
		for i, item := range val {
			val[i] = normalizeYAMLMap(item)
		}
		return val
	default:
		return v
	}
}

// parseScriptHeader 解析 @script 行头，提取脚本名称
// 支持: @script onRequest, @script onResponse, @script onRequest file://path.js
func (p *Parser) parseScriptHeader(line string) (scriptName string, isExternal bool, externalPath string) {
	rest := strings.TrimPrefix(line, "@script")
	rest = strings.TrimSpace(rest)

	// 检查是否引用外部文件
	if idx := strings.Index(rest, "file://"); idx >= 0 {
		scriptName = strings.TrimSpace(rest[:idx])
		externalPath = strings.TrimSpace(rest[idx+len("file://"):])
		isExternal = true
		return
	}

	scriptName = rest
	return
}

// parseDirective 解析 @ 指令
func (p *Parser) parseDirective(script *RuleScript, line string, lineNo int, rawLine string) error {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return fmt.Errorf("empty directive")
	}

	directive := parts[0]
	switch directive {
	case "@name":
		// @name "名称" 或 @name 名称
		name := strings.TrimPrefix(line, "@name")
		name = strings.TrimSpace(name)
		name = strings.Trim(name, `"'`)
		script.Name = name

	case "@priority":
		if len(parts) < 2 {
			return fmt.Errorf("@priority requires a value")
		}
		var priority int
		if _, err := fmt.Sscanf(parts[1], "%d", &priority); err != nil {
			return fmt.Errorf("@priority value must be integer: %s", parts[1])
		}
		script.Priority = priority

	case "@intercept":
		if len(parts) < 2 {
			return fmt.Errorf("@intercept requires at least one domain")
		}
		script.Intercepts = append(script.Intercepts, parts[1:]...)

	case "@timeout":
		// 全局超时，暂存到 metadata（Sprint 2 实现）

	default:
		return fmt.Errorf("unknown directive: %s", directive)
	}

	return nil
}

// parseRegexRule 解析正则规则：/regex/ target [@options]
// 策略：从右侧定位 target（http:// | https:// | pipeline://），
//
//	target 左侧空格前的最后一个 / 就是正则结尾分隔符。
//	正则内部的 / 无需转义（但转义了也兼容）。
func (p *Parser) parseRegexRule(line string, lineNo int, rawLine string) (*Rule, error) {
	// 1. 定位 target：找到以空格分隔的 http:// | https:// | pipeline:// 部分
	targetIdx := p.findTargetIndex(line)
	if targetIdx < 0 {
		return nil, fmt.Errorf("regex rule requires a target (http://, https://, or pipeline://)")
	}

	// 2. target 及其后面的 options
	rest := line[targetIdx:]
	beforeTarget := line[:targetIdx]

	// 3. 去除 target 前的空白，然后向左找正则结尾分隔符 /
	beforeTarget = strings.TrimRight(beforeTarget, " \t")
	if len(beforeTarget) < 2 || beforeTarget[0] != '/' || beforeTarget[len(beforeTarget)-1] != '/' {
		return nil, fmt.Errorf("regex not properly delimited (expected /regex/ before target)")
	}

	// 4. 提取正则内容（去掉首尾 /）
	patternStr := beforeTarget[1 : len(beforeTarget)-1]
	if patternStr == "" {
		return nil, fmt.Errorf("empty regex pattern")
	}

	// 5. 兼容处理：将 \/ 统一还原为 /（用户可以选择转义或不转义）
	patternStr = strings.ReplaceAll(patternStr, `\/`, `/`)

	// 6. 编译正则
	re, err := regexp.Compile(patternStr)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}

	// 7. 解析 target 和 options
	parts := strings.Fields(rest)
	target := parts[0]

	rule := &Rule{
		Line: lineNo,
		Raw:  line,
	}

	// 判断 target 协议
	switch {
	case strings.HasPrefix(target, "pipeline://"):
		rule.Type = RuleTypePipeline
		rule.Pattern = re
		rule.Adapter = strings.TrimPrefix(target, "pipeline://")
	case strings.HasPrefix(target, "script://"):
		rule.Type = RuleTypeScript
		rule.Pattern = re
		rule.ScriptRef = strings.TrimPrefix(target, "script://")
	default:
		rule.Type = RuleTypeProxy
		rule.Pattern = re
		rule.Target = target
	}

	// 解析 options
	for _, opt := range parts[1:] {
		switch {
		case opt == "@stream":
			rule.Stream = true
		case opt == "@cors":
			rule.CORS = true
		case strings.HasPrefix(opt, "@timeout="):
			// 暂不解析具体值，Sprint 2 完善
		}
	}

	return rule, nil
}

// findTargetIndex 从行中定位 target 的起始位置
// target 必须以空格或制表符为前缀（区分正则内部可能含有的 http://）
// 支持协议：http://, https://, pipeline://, script://
func (p *Parser) findTargetIndex(line string) int {
	protocols := []string{"http://", "https://", "pipeline://", "script://"}

	// 从右向左搜索，优先匹配最靠右的 target（正则内容在左侧）
	bestIdx := -1
	for _, proto := range protocols {
		idx := strings.LastIndex(line, proto)
		for idx > 0 {
			// target 前面必须是空白字符（确保不是正则内容的一部分）
			if line[idx-1] == ' ' || line[idx-1] == '\t' {
				if bestIdx < 0 || idx > bestIdx {
					bestIdx = idx
				}
				break
			}
			// 继续向左查找同一个协议的更早出现
			idx = strings.LastIndex(line[:idx], proto)
		}
	}

	return bestIdx
}

// parseHostsOrExact 解析 hosts 映射或精确路径规则
func (p *Parser) parseHostsOrExact(line string, lineNo int, rawLine string) (*Rule, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nil, fmt.Errorf("invalid rule format")
	}

	first := fields[0]

	// 判断是否是 IP 格式（hosts 映射）
	if isIPLike(first) {
		// Hosts 映射：IP domain1 domain2 ...
		domains := make([]string, 0, len(fields)-1)
		for _, d := range fields[1:] {
			// 跳过包含路径或协议的条目
			if strings.Contains(d, "/") || strings.Contains(d, "://") {
				continue
			}
			domains = append(domains, d)
		}
		if len(domains) == 0 {
			return nil, fmt.Errorf("hosts rule requires at least one domain")
		}
		return &Rule{
			Type:    RuleTypeHosts,
			Line:    lineNo,
			Raw:     line,
			IP:      first,
			Domains: domains,
		}, nil
	}

	// 精确路径代理：domain/path target
	if strings.Contains(first, "/") {
		if len(fields) < 2 {
			return nil, fmt.Errorf("exact rule requires a target")
		}
		// 拆分 domain 和 path
		slashIdx := strings.Index(first, "/")
		domain := first[:slashIdx]
		path := first[slashIdx:]
		target := fields[1]

		return &Rule{
			Type:   RuleTypeExact,
			Line:   lineNo,
			Raw:    line,
			Domain: domain,
			Path:   path,
			Target: target,
		}, nil
	}

	return nil, fmt.Errorf("unrecognized rule format: %s", first)
}

// isIPLike 判断字符串是否看起来像 IP 地址
func isIPLike(s string) bool {
	// 简单判断：包含 . 且第一个字符是数字，或者是 IPv6 格式
	if len(s) == 0 {
		return false
	}
	if s[0] >= '0' && s[0] <= '9' && strings.Contains(s, ".") {
		return true
	}
	// IPv6
	if strings.Contains(s, ":") && !strings.Contains(s, "/") {
		return true
	}
	return false
}

// ========================================
// Column 计算 & Suggestion 辅助方法
// ========================================

// computeRegexErrorColumn 根据 rawLine 和错误信息计算正则规则错误的列号
func (p *Parser) computeRegexErrorColumn(rawLine string, err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "requires a target"):
		// 正则末尾后找不到 target，指向正则结尾 / 之后
		// 从 rawLine 中找最后一个 /（正则结尾分隔符）
		targetIdx := p.findTargetIndex(rawLine)
		if targetIdx < 0 {
			// 找不到 target，指向第一个 / 的位置
			if idx := strings.Index(rawLine, "/"); idx >= 0 {
				return idx + 1
			}
		}
		return 1
	case strings.Contains(msg, "not properly delimited"):
		// 正则格式不对，指向第一个 /
		if idx := strings.Index(rawLine, "/"); idx >= 0 {
			return idx + 1
		}
		return 1
	case strings.Contains(msg, "empty regex pattern"):
		// 空正则 // ，指向第一个 / 之后
		if idx := strings.Index(rawLine, "//"); idx >= 0 {
			return idx + 2
		}
		return 1
	case strings.Contains(msg, "invalid regex"):
		// 正则语法错误，指向第一个 /
		if idx := strings.Index(rawLine, "/"); idx >= 0 {
			return idx + 1
		}
		return 1
	}
	return 1
}

// computeRegexSuggestion 根据正则规则错误提供修复建议
func (p *Parser) computeRegexSuggestion(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "requires a target"):
		return "Add an http://, https://, or script:// target after the regex pattern"
	case strings.Contains(msg, "not properly delimited"):
		return "Wrap the regex pattern with slashes, e.g. /api/v1/"
	case strings.Contains(msg, "empty regex pattern"):
		return "Add a regex pattern between the slashes, e.g. /api/v1/"
	case strings.Contains(msg, "invalid regex"):
		return "Check regex syntax. Common issues: unescaped special chars, unmatched parentheses"
	}
	return ""
}

// computeDirectiveErrorColumn 根据 rawLine 和错误信息计算指令错误的列号
func (p *Parser) computeDirectiveErrorColumn(rawLine string, err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "@priority requires a value"):
		// @priority 后面的位置
		if idx := strings.Index(rawLine, "@priority"); idx >= 0 {
			return idx + len("@priority") + 1
		}
	case strings.Contains(msg, "@priority value must be integer"):
		// @priority 后面的值位置
		if idx := strings.Index(rawLine, "@priority"); idx >= 0 {
			return idx + len("@priority") + 1
		}
	case strings.Contains(msg, "unknown directive"):
		// 未知指令的起始位置
		if idx := strings.Index(rawLine, "@"); idx >= 0 {
			return idx + 1
		}
	}
	return 1
}

// computeDirectiveSuggestion 根据指令错误提供修复建议
func (p *Parser) computeDirectiveSuggestion(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown directive"):
		return "Valid directives: @name, @priority, @intercept, @config, @script, @timeout"
	case strings.Contains(msg, "@priority requires a value"):
		return "Example: @priority 100"
	case strings.Contains(msg, "@priority value must be integer"):
		return "Priority must be a positive integer, e.g. @priority 100"
	}
	return ""
}

// computeHostsErrorColumn 根据 rawLine 和错误信息计算 hosts/exact 规则错误的列号
func (p *Parser) computeHostsErrorColumn(rawLine string, err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "hosts rule requires at least one domain"):
		// IP 后面的位置
		fields := strings.Fields(rawLine)
		if len(fields) > 0 {
			return len(fields[0]) + 1
		}
	case strings.Contains(msg, "exact rule requires a target"):
		// domain/path 后面的位置
		fields := strings.Fields(rawLine)
		if len(fields) > 0 {
			return len(fields[0]) + 1
		}
	case strings.Contains(msg, "invalid rule format"):
		return 1
	}
	return 1
}

// computeHostsSuggestion 根据 hosts/exact 规则错误提供修复建议
func (p *Parser) computeHostsSuggestion(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "hosts rule requires at least one domain"):
		return "Format: IP_ADDRESS domain1 domain2 ..."
	case strings.Contains(msg, "exact rule requires a target"):
		return "Format: domain/path http://target-url"
	case strings.Contains(msg, "invalid rule format"):
		return "Valid formats: /regex/ target, IP domain1 domain2, or domain/path target"
	}
	return ""
}
