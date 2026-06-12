// Package provider wraps the anthropic api. Gen-0 ships exactly one
// implementation; the interface is the seam gen-2's provider layer
// (neutral message format, openai-compat) replaces.
package provider

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
)

// Provider is the gen-0 seam. It leaks sdk types on purpose: a neutral
// message format is gen-2 scope (plan §5), not tonight's.
type Provider interface {
	Complete(ctx context.Context, model string, system []anthropic.TextBlockParam,
		msgs []anthropic.MessageParam, tools []anthropic.ToolUnionParam, maxTokens int64) (*anthropic.Message, error)
	ContextWindow(model string) int64
}

type Anthropic struct {
	client anthropic.Client
}

func NewAnthropic() *Anthropic {
	return &Anthropic{client: anthropic.NewClient()} // ANTHROPIC_API_KEY from env
}

func (a *Anthropic) Complete(ctx context.Context, model string, system []anthropic.TextBlockParam,
	msgs []anthropic.MessageParam, tools []anthropic.ToolUnionParam, maxTokens int64) (*anthropic.Message, error) {
	return a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Tools:     tools,
	})
}

// ContextWindow returns the assumed input window for ctx_pct tracking.
// Conservative 200k default; gen-1 can read the models api instead.
func (a *Anthropic) ContextWindow(model string) int64 { return 200_000 }

// CtxPct computes context consumption from response usage: everything the
// next request will carry as input, as a percentage of the window.
func CtxPct(u anthropic.Usage, window int64) int {
	used := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
	return int(used * 100 / window)
}
