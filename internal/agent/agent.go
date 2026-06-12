// Package agent implements the gen-0 blocking loop (plan §3.6).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aliirz/raqim/internal/permission"
	"github.com/aliirz/raqim/internal/provider"
	"github.com/aliirz/raqim/internal/session"
	"github.com/aliirz/raqim/internal/tools"
)

// ErrContextLimit signals the 95% hard stop; the caller ends the session
// with reason context_limit and runs the echo pass.
var ErrContextLimit = errors.New("context limit reached")

const systemPrompt = `you are raqim, a coding harness with persistent memory. you work inside the user's repository: read code before changing it, make minimal correct changes, and verify with the project's own tools (tests, builds) when you can.

tools: read, write, edit, bash, memory_search. tool calls run serially and may be denied by the user; a denial includes their reason — adapt to it instead of retrying. keep bash commands non-interactive.

memory: a memory index for this project is injected below. it is distilled from past sessions and is trustworthy context. use memory_search before re-deriving facts that may already be known. durable knowledge you establish this session (decisions, gotchas, references) will be distilled into memory when the session ends — so state important conclusions clearly.

be concise. lead with the outcome. when the task is done, stop.`

type Agent struct {
	Prov    provider.Provider
	Model   string
	Perm    *permission.Engine
	Sess    *session.Session
	ToolCtx *tools.Ctx
	Out     io.Writer
	WarnPct int

	system []anthropic.TextBlockParam
	msgs   []anthropic.MessageParam
	warned bool
}

// Init freezes the system prompt + injection block for the session
// (invariant: injection immutable per session). The cache breakpoint sits
// after the injection, ending the byte-stable prefix.
func (a *Agent) Init(injection string) {
	a.system = []anthropic.TextBlockParam{
		{Text: systemPrompt},
		{Text: injection, CacheControl: anthropic.NewCacheControlEphemeralParam()},
	}
}

// Turn runs one user turn to completion (text-only response).
func (a *Agent) Turn(ctx context.Context, userText string) error {
	a.Sess.User(userText)
	a.msgs = append(a.msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(userText)))

	for {
		resp, err := a.Prov.Complete(ctx, a.Model, a.system, a.msgs, tools.Definitions(), 8192)
		if err != nil {
			return err
		}
		pct := provider.CtxPct(resp.Usage, a.Prov.ContextWindow(a.Model))
		a.Sess.Usage(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadInputTokens, pct)

		a.msgs = append(a.msgs, resp.ToParam())
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				fmt.Fprintln(a.Out, v.Text)
				a.Sess.Assistant(v.Text)
			case anthropic.ToolUseBlock:
				args := json.RawMessage(v.JSON.Input.Raw())
				a.Sess.ToolCall(v.ID, v.Name, args)
				out, ok := a.runTool(v.Name, args)
				a.Sess.ToolResult(v.ID, ok, out)
				if len(out) > session.OutputCap {
					out = out[:session.OutputCap] + "\n[output truncated]"
				}
				results = append(results, anthropic.NewToolResultBlock(v.ID, out, !ok))
			}
		}

		if pct >= 95 {
			return ErrContextLimit
		}
		if pct >= a.WarnPct && !a.warned {
			a.warned = true
			fmt.Fprintf(a.Out, "\n[context at %d%%. consider /exit; memory will carry context to a fresh session.]\n", pct)
		}
		if resp.StopReason != anthropic.StopReasonToolUse {
			return nil
		}
		a.msgs = append(a.msgs, anthropic.NewUserMessage(results...))
	}
}

func (a *Agent) runTool(name string, args json.RawMessage) (string, bool) {
	summary := tools.Summary(name, args)
	d := a.Perm.Check(name, summary)
	if !d.Allowed {
		fb := d.Feedback
		if fb == "" {
			fb = "no reason given"
		}
		return "denied by user: " + fb, false
	}
	return tools.Execute(a.ToolCtx, name, args)
}
