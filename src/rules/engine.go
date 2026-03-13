package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"mitmcdn/src/config"
)

type Action string

const (
	ActionCache  Action = "cache"
	ActionBypass Action = "bypass"
	ActionDeny   Action = "deny"
)

type Scope string

const (
	ScopeRequestOnly  Scope = "request_only"
	ScopeResponseInfo Scope = "response_info"
	ScopeResponseData Scope = "response_data"
)

type Decision struct {
	Action        Action
	DedupStrategy string
	CacheKey      string
	TTLOverride   time.Duration
	Priority      int
	MaxSize       int64
	RuleName      string
}

type Engine struct {
	rules []compiledRule
}

type compiledRule struct {
	name          string
	action        Action
	dedupStrategy string
	priority      int
	maxSize       int64
	ttlOverride   time.Duration
	scope         Scope
	program       *vm.Program
	hashExpr      *vm.Program
}

// NewEngine compiles cache rules into an executable engine.
func NewEngine(rules []config.CacheRule) (*Engine, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for i, rule := range rules {
		name := rule.Name
		if name == "" {
			name = fmt.Sprintf("rule-%d", i+1)
		}

		action := Action(strings.ToLower(rule.Action))
		if action == "" {
			action = ActionCache
		}
		switch action {
		case ActionCache, ActionBypass, ActionDeny:
		default:
			return nil, fmt.Errorf("invalid action for rule %s: %s", name, rule.Action)
		}

		dedupStrategy := rule.DedupStrategy
		if dedupStrategy == "" {
			dedupStrategy = "full_url"
		}

		scope := ScopeRequestOnly
		if rule.Scope != "" {
			scope = Scope(strings.ToLower(rule.Scope))
		}
		if scope != ScopeRequestOnly && scope != ScopeResponseInfo && scope != ScopeResponseData {
			return nil, fmt.Errorf("invalid scope for rule %s: %s", name, rule.Scope)
		}

		var ttlOverride time.Duration
		if rule.TTLOverride != "" {
			parsed, err := config.ParseDuration(rule.TTLOverride)
			if err != nil {
				return nil, fmt.Errorf("invalid ttl_override for rule %s: %w", name, err)
			}
			ttlOverride = parsed
		}

		var maxSize int64
		if rule.MaxSize != "" {
			parsed, err := config.ParseSize(rule.MaxSize)
			if err != nil {
				return nil, fmt.Errorf("invalid max_size for rule %s: %w", name, err)
			}
			maxSize = parsed
		}

		if strings.TrimSpace(rule.Expr) == "" {
			return nil, fmt.Errorf("missing expr for rule %s", name)
		}

		baseEnv := baseEnvironment()
		program, err := expr.Compile(rule.Expr, expr.Env(baseEnv), expr.AsBool())
		if err != nil {
			return nil, fmt.Errorf("failed to compile expr for rule %s: %w", name, err)
		}

		var hashProgram *vm.Program
		if strings.TrimSpace(rule.HashExpr) != "" {
			hashProgram, err = expr.Compile(rule.HashExpr, expr.Env(baseEnv))
			if err != nil {
				return nil, fmt.Errorf("failed to compile hash_expr for rule %s: %w", name, err)
			}
			if !strings.EqualFold(dedupStrategy, "hash_expr") {
				return nil, fmt.Errorf("hash_expr requires dedup_strategy=hash_expr for rule %s", name)
			}
		}
		if strings.EqualFold(dedupStrategy, "hash_expr") && hashProgram == nil {
			return nil, fmt.Errorf("missing hash_expr for rule %s", name)
		}

		compiled = append(compiled, compiledRule{
			name:          name,
			action:        action,
			dedupStrategy: dedupStrategy,
			priority:      rule.Priority,
			maxSize:       maxSize,
			ttlOverride:   ttlOverride,
			scope:         scope,
			program:       program,
			hashExpr:      hashProgram,
		})
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		return compiled[i].priority > compiled[j].priority
	})

	return &Engine{rules: compiled}, nil
}

func (e *Engine) HasRules() bool {
	return e != nil && len(e.rules) > 0
}

type ResponseFetcher func(scope Scope) (*ResponseData, error)

func (e *Engine) Evaluate(req RequestData, fetcher ResponseFetcher) (*Decision, bool, error) {
	if e == nil || len(e.rules) == 0 {
		return nil, false, nil
	}

	for _, rule := range e.rules {
		var resp *ResponseData
		switch rule.scope {
		case ScopeRequestOnly:
			resp = nil
		case ScopeResponseInfo, ScopeResponseData:
			if fetcher == nil {
				return nil, false, fmt.Errorf("response fetcher missing for rule %s", rule.name)
			}
			fetched, err := fetcher(rule.scope)
			if err != nil {
				return nil, false, fmt.Errorf("rule %s response fetch error: %w", rule.name, err)
			}
			resp = fetched
		default:
			return nil, false, fmt.Errorf("rule %s has invalid scope: %s", rule.name, rule.scope)
		}

		env := buildEnvironment(req, resp)
		out, err := expr.Run(rule.program, env)
		if err != nil {
			return nil, false, fmt.Errorf("rule %s evaluation error: %w", rule.name, err)
		}
		matched, ok := out.(bool)
		if !ok || !matched {
			continue
		}

		decision := &Decision{
			Action:        rule.action,
			DedupStrategy: rule.dedupStrategy,
			TTLOverride:   rule.ttlOverride,
			Priority:      rule.priority,
			MaxSize:       rule.maxSize,
			RuleName:      rule.name,
		}
		if rule.hashExpr != nil {
			keyOut, err := expr.Run(rule.hashExpr, env)
			if err != nil {
				return nil, false, fmt.Errorf("rule %s hash_expr error: %w", rule.name, err)
			}
			key, ok := keyOut.(string)
			if !ok {
				return nil, false, fmt.Errorf("rule %s hash_expr did not return string", rule.name)
			}
			decision.CacheKey = key
		}

		return decision, true, nil
	}

	return nil, false, nil
}
