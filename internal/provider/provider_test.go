package provider

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestCtxPct(t *testing.T) {
	const window = 200_000

	tests := []struct {
		name  string
		usage anthropic.Usage
		want  int
	}{
		{
			// Real session event: in:5175 cache_read:62524 out:44
			// (5175 + 62524) / 200000 = 33.849 → 33
			// output tokens must NOT be included in numerator
			name:  "real_shaped_cache_heavy",
			usage: anthropic.Usage{InputTokens: 5175, CacheReadInputTokens: 62524, OutputTokens: 44},
			want:  33,
		},
		{
			// No cache — plain input only
			// 100000 / 200000 = 50
			name:  "no_cache",
			usage: anthropic.Usage{InputTokens: 100_000, OutputTokens: 8192},
			want:  50,
		},
		{
			// Cache creation turn (first time system prompt is cached)
			// in:1000 cache_creation:60000 out:500
			// (1000 + 60000) / 200000 = 30.5 → 30
			name:  "cache_creation",
			usage: anthropic.Usage{InputTokens: 1_000, CacheCreationInputTokens: 60_000, OutputTokens: 500},
			want:  30,
		},
		{
			// All three token types present
			// in:2000 cache_read:50000 cache_creation:10000 out:1000
			// (2000 + 50000 + 10000) / 200000 = 31
			name:  "all_token_types",
			usage: anthropic.Usage{InputTokens: 2_000, CacheReadInputTokens: 50_000, CacheCreationInputTokens: 10_000, OutputTokens: 1_000},
			want:  31,
		},
		{
			// Near trigger threshold — large output must not push past it
			// in:160000 cache_read:0 cache_creation:0 out:8000
			// 160000 / 200000 = 80 (output excluded, stays below 85)
			name:  "large_output_excluded",
			usage: anthropic.Usage{InputTokens: 160_000, OutputTokens: 8_000},
			want:  80,
		},
		{
			// Zero usage (fresh session first turn)
			name:  "zero",
			usage: anthropic.Usage{},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CtxPct(tt.usage, window)
			if got != tt.want {
				t.Errorf("CtxPct(%+v, %d) = %d, want %d", tt.usage, window, got, tt.want)
			}
		})
	}
}
