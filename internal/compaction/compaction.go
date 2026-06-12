// Package compaction implements gen-1 context compaction (plan §4.1).
//
// Trigger: provider-reported ctx_pct ≥ 85.
// Handoff: the agent model writes a structured summary (task state, decisions,
//
//	files touched, next steps) via a dedicated completion call.
//
// Provider view after compaction:
//
//	injection block (verbatim, cache prefix preserved)
//	+ handoff block
//	+ last 3 turns of real conversation
//
// Invariants (plan §7): the jsonl transcript is never modified; echo always
// reads the full, uncompacted transcript.
package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aliirz/raqim/internal/provider"
)

// TriggerPct is the ctx_pct threshold that triggers compaction.
const TriggerPct = 85

// TailTurns is the number of recent conversation turns preserved verbatim
// in the provider view after compaction.
const TailTurns = 3

// handoffPrompt is sent to the agent model to produce the structured handoff.
const handoffPrompt = `you are mid-session and context is getting full. write a structured compaction handoff that will replace the conversation history. it will be the only context the next window has (besides the memory injection), so be complete and precise.

include:
- TASK STATE: what we are doing right now, what has been completed, what is blocked
- DECISIONS: key choices made this session and why (one line each)
- FILES TOUCHED: paths read or modified, and what changed
- NEXT STEPS: concrete, ordered list of what to do next

be terse. this is not a summary for a human; it is a machine-readable handoff. no preamble, no sign-off. output only the handoff text.`

// RearmPct is the ctx_pct below which the compactor re-arms after a
// successful compaction, allowing it to fire again if the session grows back
// above TriggerPct. The guard's only job is to prevent an immediate
// re-trigger on the same high-pct response before the view has been replaced.
const RearmPct = 60

// Compactor manages compaction state for one agent session.
type Compactor struct {
	// pending is true after Run() succeeds and before ctx_pct has dropped
	// below RearmPct. It blocks re-trigger on the same high-pct response.
	pending bool
	// Handoff is the structured handoff text produced by the most recent
	// successful compaction. Empty until compaction runs.
	Handoff string
}

// ShouldTrigger reports whether compaction should fire given the current
// context percentage. It re-arms (and can fire again) once a usage report
// shows ctx_pct has dropped below RearmPct after a previous compaction.
func (c *Compactor) ShouldTrigger(ctxPct int) bool {
	if c.pending && ctxPct < RearmPct {
		c.pending = false
	}
	return !c.pending && ctxPct >= TriggerPct
}

// Run calls the agent model to produce a handoff, stores it in c.Handoff,
// and sets c.Triggered. The caller must record the compaction event in the
// session jsonl separately (invariant: jsonl unaffected by the view change).
func (c *Compactor) Run(
	ctx context.Context,
	prov provider.Provider,
	model string,
	system []anthropic.TextBlockParam,
	msgs []anthropic.MessageParam,
) error {
	// Ask the model to summarise everything into a handoff block.
	compactMsgs := append(msgs, anthropic.NewUserMessage(
		anthropic.NewTextBlock(handoffPrompt),
	))
	resp, err := prov.Complete(ctx, model, system, compactMsgs, nil, 2048)
	if err != nil {
		return fmt.Errorf("compaction call failed: %w", err)
	}
	var sb strings.Builder
	for _, blk := range resp.Content {
		// Prefer direct field access: ContentBlockUnion.Text is always set for
		// text blocks regardless of whether the struct came from JSON or a stub.
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	c.Handoff = strings.TrimSpace(sb.String())
	c.pending = true
	return nil
}

// AssembleProviderView builds the message slice the agent uses after
// compaction: a synthetic user+assistant pair carrying the handoff, followed
// by the last TailTurns turns from the real history.
//
// "Turn" here means one user+assistant exchange (two consecutive messages
// in Anthropic's alternating format). If the history is shorter than
// TailTurns pairs, all of it is kept.
func (c *Compactor) AssembleProviderView(msgs []anthropic.MessageParam) []anthropic.MessageParam {
	handoffMsg := anthropic.NewUserMessage(
		anthropic.NewTextBlock("[COMPACTION HANDOFF]\n" + c.Handoff),
	)
	ack := anthropic.MessageParam{
		Role: anthropic.MessageParamRoleAssistant,
		Content: []anthropic.ContentBlockParamUnion{
			anthropic.NewTextBlock("understood. continuing from handoff."),
		},
	}

	tail := tailTurns(msgs, TailTurns)
	out := make([]anthropic.MessageParam, 0, 2+len(tail))
	out = append(out, handoffMsg, ack)
	out = append(out, tail...)
	return out
}

// contentTypes returns the set of block type strings in a message's content.
func contentTypes(m anthropic.MessageParam) map[string]bool {
	out := map[string]bool{}
	for _, blk := range m.Content {
		raw, err := json.Marshal(blk)
		if err != nil {
			continue
		}
		var obj struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil || obj.Type == "" {
			continue
		}
		out[obj.Type] = true
	}
	return out
}

// isClosed reports whether an assistant message is a valid exchange closer:
// it must have role=assistant and contain NO tool_use blocks.
// An assistant message with tool_use blocks is mid-exchange (awaiting
// tool_result) and must never be the last message sent to the API.
func isClosed(m anthropic.MessageParam) bool {
	return m.Role == anthropic.MessageParamRoleAssistant && !contentTypes(m)["tool_use"]
}

// isToolResultMsg reports whether a user message contains only tool_result
// blocks. Such messages are mid-exchange and must never appear without the
// assistant(tool_use) that preceded them.
func isToolResultMsg(m anthropic.MessageParam) bool {
	if m.Role != anthropic.MessageParamRoleUser {
		return false
	}
	types := contentTypes(m)
	return len(types) > 0 && types["tool_result"] && !types["text"]
}

// tailTurns returns the last n complete exchanges from msgs.
//
// An exchange is the contiguous slice from a real user message
// (isInitiatingUser) through the final isClosed assistant of that exchange,
// including all interleaved tool_use/tool_result pairs in between.
//
//	plain:      user(text) → assistant(text)
//	tool-using: user(text) → assistant(tool_use) → user(tool_result) → … → assistant(text)
//
// Incomplete fragments at the tail of the history are excluded entirely —
// the API hard-rejects both:
//
//	assistant(tool_use) with no following tool_result  → excluded
//	user(tool_result) with no following isClosed assistant → excluded
//
// Walking backwards: scan for the rightmost isClosed assistant (exchange
// end). Walk left consuming interleaved tool_result/tool_use-assistant pairs
// — each intermediate assistant must be !isClosed (still mid-tool-use).
// Stop at the first isInitiatingUser message: that is the exchange start.
func tailTurns(msgs []anthropic.MessageParam, n int) []anthropic.MessageParam {
	if len(msgs) == 0 || n <= 0 {
		return nil
	}

	type exchange struct{ start, end int } // inclusive indices into msgs
	var exchanges []exchange               // collected newest-first

	i := len(msgs) - 1
	for i >= 0 && len(exchanges) < n {
		// Find the rightmost isClosed assistant — the end of a complete exchange.
		if !isClosed(msgs[i]) {
			i--
			continue
		}
		end := i
		i--

		// Walk left over interleaved tool_result / tool_use-assistant pairs.
		// Each such pair is: user(tool_result) at i, assistant(tool_use) at i-1.
		// Stop as soon as we see anything else.
		for i >= 1 && isToolResultMsg(msgs[i]) && msgs[i-1].Role == anthropic.MessageParamRoleAssistant && !isClosed(msgs[i-1]) {
			i -= 2
		}

		// msgs[i] must now be the initiating user (non-tool_result) message.
		if i < 0 || !isInitiatingUser(msgs[i]) {
			// Malformed or partial history before this point; skip this exchange.
			continue
		}
		exchanges = append(exchanges, exchange{i, end})
		i--
	}

	// Reverse to chronological order.
	for l, r := 0, len(exchanges)-1; l < r; l, r = l+1, r-1 {
		exchanges[l], exchanges[r] = exchanges[r], exchanges[l]
	}

	var out []anthropic.MessageParam
	for _, ex := range exchanges {
		out = append(out, msgs[ex.start:ex.end+1]...)
	}
	return out
}

// isInitiatingUser reports whether a message is a valid exchange opener:
// role=user and NOT a tool_result message.
func isInitiatingUser(m anthropic.MessageParam) bool {
	return m.Role == anthropic.MessageParamRoleUser && !isToolResultMsg(m)
}
