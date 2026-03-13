package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type CacheRule struct {
	Name          string `toml:"name"`
	Scope         string `toml:"scope"`
	Expr          string `toml:"expr"`
	Action        string `toml:"action"`
	DedupStrategy string `toml:"dedup_strategy"`
	HashExpr      string `toml:"hash_expr"`
	TTLOverride   string `toml:"ttl_override"`
	Priority      int    `toml:"priority"`
	MaxSize       string `toml:"max_size"`
}

// LoadCacheRules loads cache rules from a directory of TOML files.
func LoadCacheRules(dir string) ([]CacheRule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache rules dir: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".toml") {
			files = append(files, filepath.Join(dir, name))
		}
	}

	sort.Strings(files)

	rules := make([]CacheRule, 0, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read cache rule %s: %w", path, err)
		}

		var rule CacheRule
		if err := toml.Unmarshal(data, &rule); err != nil {
			return nil, fmt.Errorf("failed to parse cache rule %s: %w", path, err)
		}

		if rule.Name == "" {
			rule.Name = strings.TrimSuffix(filepath.Base(path), ".toml")
		}

		rules = append(rules, rule)
	}

	return rules, nil
}
