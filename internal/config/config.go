// Package config loads ~/.raqim/config.toml (harness) and the memory
// repo's config.toml (budgets, session ttl).
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Model    Model               `toml:"model"`
	Projects map[string][]string `toml:"projects"`
	Memory   Memory              `toml:"memory"`
	Context  Context             `toml:"context"`

	path string `toml:"-"`
}

type Model struct {
	Agent string `toml:"agent"`
	Echo  string `toml:"echo"`
}

type Memory struct {
	Path string `toml:"path"`
}

type Context struct {
	WarnPct    int `toml:"warn_pct"`
	CompactPct int `toml:"compact_pct"`
}

// MemConfig is the memory repo's own config.toml (memory-spec §8).
type MemConfig struct {
	Budgets struct {
		IndexTokens        int `toml:"index_tokens"`
		InjectTokens       int `toml:"inject_tokens"`
		EntryFirstParaWords int `toml:"entry_first_para_words"`
	} `toml:"budgets"`
	Sessions struct {
		TTLDays             int  `toml:"ttl_days"`
		DistillBeforeDelete bool `toml:"distill_before_delete"`
	} `toml:"sessions"`
}

func RaqimDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".raqim")
}

func ExpandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return p
}

// Load reads the harness config, creating a default one on first run.
func Load() (*Config, error) {
	path := filepath.Join(RaqimDir(), "config.toml")
	cfg := &Config{path: path}
	cfg.Model.Agent = "claude-sonnet-4-6"
	cfg.Model.Echo = "claude-haiku-4-5"
	cfg.Memory.Path = "~/.raqim/memory"
	cfg.Context.WarnPct = 80
	cfg.Context.CompactPct = 85
	cfg.Projects = map[string][]string{}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Context.WarnPct == 0 {
		cfg.Context.WarnPct = 80
	}
	if cfg.Context.CompactPct == 0 {
		cfg.Context.CompactPct = 85
	}
	return cfg, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func (c *Config) MemoryPath() string { return ExpandTilde(c.Memory.Path) }

// LoadMemConfig reads memory/config.toml, applying spec defaults when absent.
func LoadMemConfig(memPath string) MemConfig {
	var mc MemConfig
	mc.Budgets.IndexTokens = 1500
	mc.Budgets.InjectTokens = 3000
	mc.Budgets.EntryFirstParaWords = 120
	mc.Sessions.TTLDays = 30
	mc.Sessions.DistillBeforeDelete = true
	toml.DecodeFile(filepath.Join(memPath, "config.toml"), &mc) // absent file keeps defaults
	return mc
}

// GitToplevel returns the git root of dir, or "" if not a repo.
func GitToplevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResolveProject maps cwd to a project name per plan §3.2.
// Returns the project name and the git root ("" when not in a repo).
// found is false when the repo is unmapped (caller prompts to map it).
func (c *Config) ResolveProject(cwd string) (project, gitRoot string, found bool) {
	gitRoot = GitToplevel(cwd)
	if gitRoot == "" {
		return "scratch", "", true
	}
	for name, paths := range c.Projects {
		for _, p := range paths {
			if ExpandTilde(p) == gitRoot {
				return name, gitRoot, true
			}
		}
	}
	return "", gitRoot, false
}

// MapRepo appends repo to project (creating it) and persists the config.
func (c *Config) MapRepo(project, repoPath string) error {
	c.Projects[project] = append(c.Projects[project], repoPath)
	return c.Save()
}
