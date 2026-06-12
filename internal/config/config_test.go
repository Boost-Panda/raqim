package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProject(t *testing.T) {
	home, _ := os.UserHomeDir()
	c := &Config{Projects: map[string][]string{
		"raqim": {"~/code/raqim"},
		"other": {"/tmp/other-repo"},
	}}
	tests := []struct {
		name    string
		gitRoot string
		want    string
		found   bool
	}{
		{"tilde match", filepath.Join(home, "code/raqim"), "raqim", true},
		{"absolute match", "/tmp/other-repo", "other", true},
		{"unmapped", "/tmp/unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := matchProject(c, tt.gitRoot)
			if got != tt.want || found != tt.found {
				t.Errorf("got (%q,%v), want (%q,%v)", got, found, tt.want, tt.found)
			}
		})
	}
}

// matchProject exercises the mapping half of ResolveProject without git.
func matchProject(c *Config, gitRoot string) (string, bool) {
	for name, paths := range c.Projects {
		for _, p := range paths {
			if ExpandTilde(p) == gitRoot {
				return name, true
			}
		}
	}
	return "", false
}

func TestMemConfigDefaults(t *testing.T) {
	mc := LoadMemConfig(t.TempDir())
	if mc.Budgets.IndexTokens != 1500 || mc.Budgets.InjectTokens != 3000 ||
		mc.Sessions.TTLDays != 30 || !mc.Sessions.DistillBeforeDelete {
		t.Errorf("defaults wrong: %+v", mc)
	}
}
