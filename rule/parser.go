package rule

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	}
	var errors []ParseError

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 空行
		if line == "" {
			continue
		}

		// 注释
		if strings.HasPrefix(line, "#") {
			continue
		}

		// 指令（@开头）
		if strings.HasPrefix(line, "@") {
			if err := p.parseDirective(script, line, lineNo); err != nil {
				errors = append(errors, ParseError{Line: lineNo, Message: err.Error(), Raw: line})
			}
			continue
		}

		// 正则规则（/开头）
		if strings.HasPrefix(line, "/") {
			rule, err := p.parseRegexRule(line, lineNo)
			if err != nil {
				errors = append(errors, ParseError{Line: lineNo, Message: err.Error(), Raw: line})
				continue
			}
			script.Rules = append(script.Rules, *rule)
			continue
		}

		// hosts 映射（IP 开头）或精确路径
		rule, err := p.parseHostsOrExact(line, lineNo)
		if err != nil {
			errors = append(errors, ParseError{Line: lineNo, Message: err.Error(), Raw: line})
			continue
		}
		script.Rules = append(script.Rules, *rule)
	}

	return script, errors
}

// parseDirective 解析 @ 指令
func (p *Parser) parseDirective(script *RuleScript, line string, lineNo int) error {
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
func (p *Parser) parseRegexRule(line string, lineNo int) (*Rule, error) {
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

	// 判断是 pipeline:// 还是普通代理
	if strings.HasPrefix(target, "pipeline://") {
		rule.Type = RuleTypePipeline
		rule.Pattern = re
		rule.Adapter = strings.TrimPrefix(target, "pipeline://")
	} else {
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
// 支持协议：http://, https://, pipeline://
func (p *Parser) findTargetIndex(line string) int {
	protocols := []string{"http://", "https://", "pipeline://"}

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
func (p *Parser) parseHostsOrExact(line string, lineNo int) (*Rule, error) {
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
