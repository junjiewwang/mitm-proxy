package rule

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Validator 规则校验器
// 对解析后的规则进行深度校验：正则合法性、适配器引用、超时合法性
type Validator struct {
	// 已知的适配器名称（可选，为空则跳过适配器引用检查）
	KnownAdapters map[string]bool
}

// NewValidator 创建校验器
func NewValidator(knownAdapters []string) *Validator {
	adapters := make(map[string]bool, len(knownAdapters))
	for _, name := range knownAdapters {
		adapters[name] = true
	}
	return &Validator{KnownAdapters: adapters}
}

// ValidateContent 校验规则内容（完整校验流程）
func (v *Validator) ValidateContent(content string) []ParseError {
	parser := &Parser{}
	script, parseErrors := parser.ParseContent(content)

	// 语法层错误已在 parser 中检出
	errors := append([]ParseError{}, parseErrors...)

	// 深度校验每条规则
	for i := range script.Rules {
		rule := &script.Rules[i]
		if errs := v.validateRule(rule); len(errs) > 0 {
			errors = append(errors, errs...)
		}
	}

	return errors
}

// validateRule 校验单条规则
func (v *Validator) validateRule(rule *Rule) []ParseError {
	var errors []ParseError

	switch rule.Type {
	case RuleTypeProxy:
		errors = append(errors, v.validateProxyRule(rule)...)
	case RuleTypePipeline:
		errors = append(errors, v.validatePipelineRule(rule)...)
	case RuleTypeHosts:
		errors = append(errors, v.validateHostsRule(rule)...)
	case RuleTypeExact:
		errors = append(errors, v.validateExactRule(rule)...)
	}

	return errors
}

// validateProxyRule 校验正则代理规则
func (v *Validator) validateProxyRule(rule *Rule) []ParseError {
	var errors []ParseError

	// 正则复杂度检查（防止灾难性回溯）
	if rule.Pattern != nil {
		patternStr := rule.Pattern.String()
		if err := checkRegexComplexity(patternStr); err != nil {
			errors = append(errors, ParseError{
				Line:     rule.Line,
				Column:   1,
				Severity: "warning",
				Message:  fmt.Sprintf("regex complexity warning: %s", err),
				Raw:      rule.Raw,
			})
		}
	}

	// 目标 URL 格式检查
	if rule.Target == "" {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  "proxy rule target is empty",
			Raw:      rule.Raw,
		})
	} else if !strings.HasPrefix(rule.Target, "http://") && !strings.HasPrefix(rule.Target, "https://") {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  fmt.Sprintf("proxy target should start with http:// or https://, got: %s", rule.Target),
			Raw:      rule.Raw,
		})
	}

	// 超时校验
	if rule.Timeout > 0 && rule.Timeout < 100*time.Millisecond {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "warning",
			Message:  fmt.Sprintf("timeout too short: %s (min 100ms)", rule.Timeout),
			Raw:      rule.Raw,
		})
	}

	return errors
}

// validatePipelineRule 校验 Pipeline 规则
func (v *Validator) validatePipelineRule(rule *Rule) []ParseError {
	var errors []ParseError

	// 适配器名称非空
	if rule.Adapter == "" {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  "pipeline rule adapter name is empty",
			Raw:      rule.Raw,
		})
		return errors
	}

	// 适配器引用检查（如果有已知列表）
	if len(v.KnownAdapters) > 0 && !v.KnownAdapters[rule.Adapter] {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "warning",
			Message:  fmt.Sprintf("unknown adapter: %q (known: %v)", rule.Adapter, v.knownAdapterList()),
			Raw:      rule.Raw,
		})
	}

	return errors
}

// validateHostsRule 校验 hosts 规则
func (v *Validator) validateHostsRule(rule *Rule) []ParseError {
	var errors []ParseError

	// IP 格式检查
	if rule.IP == "" {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  "hosts rule IP is empty",
			Raw:      rule.Raw,
		})
	}

	// 域名列表非空
	if len(rule.Domains) == 0 {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  "hosts rule requires at least one domain",
			Raw:      rule.Raw,
		})
	}

	return errors
}

// validateExactRule 校验精确路径规则
func (v *Validator) validateExactRule(rule *Rule) []ParseError {
	var errors []ParseError

	if rule.Target == "" {
		errors = append(errors, ParseError{
			Line:     rule.Line,
			Column:   1,
			Severity: "error",
			Message:  "exact rule target is empty",
			Raw:      rule.Raw,
		})
	}

	return errors
}

// checkRegexComplexity 检查正则复杂度（简单启发式）
func checkRegexComplexity(pattern string) error {
	// 检测嵌套量词（如 (a+)+ 可能导致灾难性回溯）
	nestedQuantifier := regexp.MustCompile(`[+*]\)+[+*]`)
	if nestedQuantifier.MatchString(pattern) {
		return fmt.Errorf("nested quantifiers detected (potential catastrophic backtracking)")
	}

	// 长度过大
	if len(pattern) > 500 {
		return fmt.Errorf("pattern too long (%d chars, max 500)", len(pattern))
	}

	return nil
}

// knownAdapterList 返回已知适配器列表
func (v *Validator) knownAdapterList() []string {
	list := make([]string, 0, len(v.KnownAdapters))
	for name := range v.KnownAdapters {
		list = append(list, name)
	}
	return list
}
