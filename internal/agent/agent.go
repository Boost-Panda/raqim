// Package agent implements the gen-0 blocking loop (plan §3.6).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aliirz/raqim/internal/compaction"
	"github.com/aliirz/raqim/internal/permission"
	"github.com/aliirz/raqim/internal/provider"
	"github.com/aliirz/raqim/internal/session"
	"github.com/aliirz/raqim/internal/tools"
)

// ErrContextLimit signals the 95% hard stop; the caller ends the session
// with reason context_limit and runs the echo pass.
var ErrContextLimit = errors.New("context limit reached")

// ErrUserExit signals /exit typed at a permission prompt; the caller ends
// the session with reason exit and runs the echo pass.
var ErrUserExit = errors.New("user ended session at permission prompt")

// ErrMaxTokens signals the response was cut off at the output token limit.
// The turn loop appends synthetic tool_results so a.msgs stays API-valid,
// then returns this error. The caller should surface it to the user.
//
// TODO(backlog): truncation currently halts the turn; for unattended/self-build
// sessions, a future version should append the synthetic result and auto-continue
// the loop so the model can finish in smaller steps without user intervention.
// See RAQIM.md backlog.
var ErrMaxTokens = errors.New("response truncated: output token limit reached")

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
	WarnPct    int
	CompactPct int
	MaxTokens  int64 // output token ceiling per request; 0 → default 16384

	system    []anthropic.TextBlockParam
	msgs      []anthropic.MessageParam // provider view: what gets sent each request
	turnMsgs  []anthropic.MessageParam // real turns since last compaction; tail source
	warned    bool
	compactor compaction.Compactor
}

// Init freezes the system prompt + injection block for the session
// (invariant: injection immutable per session). The cache breakpoint sits
// after the injection, ending the byte-stable prefix.
func (a *Agent) Init(injection string) {
	a.system = []anthropic.TextBlockParam{
		{Text: systemPrompt},
		{Text: injection, CacheControl: anthropic.NewCacheControlEphemeralParam()},
	}
	if a.CompactPct > 0 {
		a.compactor.TriggerPct = a.CompactPct
	}
}

// Turn runs one user turn to completion (text-only response).
func (a *Agent) Turn(ctx context.Context, userText string) error {
	a.Sess.User(userText)
	userMsg := anthropic.NewUserMessage(anthropic.NewTextBlock(userText))
	a.msgs = append(a.msgs, userMsg)
	a.turnMsgs = append(a.turnMsgs, userMsg)

	for {
		resp, err := a.Prov.Complete(ctx, a.Model, a.system, a.msgs, tools.Definitions(), a.maxTokens())
		if err != nil {
			return err
		}
		pct := provider.CtxPct(resp.Usage, a.Prov.ContextWindow(a.Model))
		a.Sess.Usage(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadInputTokens, pct)

		respParam := resp.ToParam()
		a.msgs = append(a.msgs, respParam)
		a.turnMsgs = append(a.turnMsgs, respParam)
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				fmt.Fprintln(a.Out, v.Text)
				a.Sess.Assistant(v.Text)
			case anthropic.ToolUseBlock:
				args := json.RawMessage(v.JSON.Input.Raw())
				a.Sess.ToolCall(v.ID, v.Name, args)
				if resp.StopReason == anthropic.StopReasonMaxTokens {
					// Do not execute — the response was truncated and tool
					// inputs are likely empty or incomplete. Emit a synthetic
					// tool_result so a.msgs stays API-valid.
					const guidance = "tool call truncated — the response hit the output token limit before the tool input was complete. this tool did NOT run. produce a smaller output or split the work into steps."
					a.Sess.ToolResult(v.ID, false, guidance)
					results = append(results, anthropic.NewToolResultBlock(v.ID, guidance, true))
				} else {
					out, ok, exit := a.runTool(v.Name, args)
					a.Sess.ToolResult(v.ID, ok, out)
					if exit {
						return ErrUserExit
					}
					if len(out) > session.OutputCap {
						out = out[:session.OutputCap] + "\n[output truncated]"
					}
					results = append(results, anthropic.NewToolResultBlock(v.ID, out, !ok))
				}
			}
		}

		// max_tokens: the model was cut off. Surface the error to the caller;
		// tool_results (if any) are already in results and must be committed
		// to a.msgs so the conversation stays API-valid for any future call.
		if resp.StopReason == anthropic.StopReasonMaxTokens {
			fmt.Fprintln(a.Out, "\n[response truncated: output token limit reached; please retry]")
			if len(results) > 0 {
				toolResultMsg := anthropic.NewUserMessage(results...)
				a.msgs = append(a.msgs, toolResultMsg)
				a.turnMsgs = append(a.turnMsgs, toolResultMsg)
			}
			return ErrMaxTokens
		}

		// §4.1 compaction: trigger at 85%, before the 95% hard stop.
		// Guard: never compact mid-tool-use. If StopReason == ToolUse the
		// assistant message just appended to turnMsgs contains a tool_use
		// block with no matching tool_result yet; AssembleProviderView would
		// produce an orphaned tool_result on the very next loop iteration.
		// ShouldTrigger is still called so the pct reading advances the
		// rearm state machine — but Run/Assemble are skipped.
		if a.compactor.ShouldTrigger(pct) && resp.StopReason != anthropic.StopReasonToolUse {
			fmt.Fprintf(a.Out, "\n[context at %d%%. running compaction...]\n", pct)
			if err := a.compactor.Run(ctx, a.Prov, a.Model, a.system, a.msgs); err != nil {
				// non-fatal: log and continue; worst case we hit 95% naturally
				fmt.Fprintf(a.Out, "[compaction failed: %v; continuing]\n", err)
			} else {
				a.msgs = a.compactor.AssembleProviderView(a.turnMsgs)
				a.turnMsgs = nil // reset: next tail window starts fresh
				a.Sess.Compaction(pct)
				fmt.Fprintf(a.Out, "[compaction done. context reset.]\n")
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
		toolResultMsg := anthropic.NewUserMessage(results...)
		a.msgs = append(a.msgs, toolResultMsg)
		a.turnMsgs = append(a.turnMsgs, toolResultMsg)
	}
}

func (a *Agent) maxTokens() int64 {
	if a.MaxTokens > 0 {
		return a.MaxTokens
	}
	return 16384
}

func (a *Agent) runTool(name string, args json.RawMessage) (out string, ok, exit bool) {
	d := a.Perm.Check(name, tools.Summary(name, args))
	if d.Exit {
		return "denied by user: session ending", false, true
	}
	if !d.Allowed {
		if d.Feedback == "" {
			return "denied by user", false, false
		}
		return "denied by user: " + d.Feedback, false, false
	}
	out, ok = tools.Execute(a.ToolCtx, name, args)
	return out, ok, false
}
