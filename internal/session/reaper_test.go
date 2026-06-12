package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(Dir(), "proj")
	os.MkdirAll(dir, 0o755)
	old := time.Now().AddDate(0, 0, -40)

	mk := func(id string, expired bool) string {
		p := filepath.Join(dir, id+".jsonl")
		os.WriteFile(p, []byte(`{"t":"meta"}`+"\n"), 0o644)
		if expired {
			os.Chtimes(p, old, old)
		}
		return p
	}
	distilledOld := mk("s-20260101-aaaa", true)
	undistilledOld := mk("s-20260101-bbbb", true)
	fresh := mk("s-20260612-cccc", false)

	var out strings.Builder
	Reap(30, func(id string) bool { return id == "s-20260101-aaaa" }, &out)

	if _, err := os.Stat(distilledOld); !os.IsNotExist(err) {
		t.Error("distilled+expired transcript should be deleted")
	}
	if _, err := os.Stat(undistilledOld); err != nil {
		t.Error("undistilled transcript must never be deleted")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh transcript must be kept")
	}
	if !strings.Contains(out.String(), "undistilled — kept") {
		t.Errorf("missing warning: %s", out.String())
	}

	und := Undistilled(func(id string) bool { return false })
	if len(und) != 2 || und["s-20260612-cccc"] != "proj" {
		t.Errorf("undistilled scan: %v", und)
	}
}
