package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aliirz/raqim/internal/provider"
)

// ── threshold trigger tests ──────────────────────────────────────────────────

func TestShouldTrigger(t *testing.T) {
	tests := []struct {
		name    string
		pending bool
		ctxPct  int
		want    bool
	}{
		{"below threshold, not pending", false, 84, false},
		{"at threshold, not pending", false, 85, true},
		{"above threshold, not pending", false, 90, true},
		{"at threshold, pending (immediate re-trigger blocked)", true, 85, false},
		{"above threshold, pending (immediate re-trigger blocked)", true, 99, false},
		{"zero pct", false, 0, false},
		{"exactly 100", false, 100, true},
		// pending clears when ctx_pct drops below RearmPct
		{"86 while pending — still blocked", true, 86, false},
		{"90 while pending — still blocked", true, 90, false},
		{"94 while pending — still blocked", true, 94, false},
		{"84 while pending — still blocked (above RearmPct)", true, 84, false},
		{"70 while pending — still blocked (above RearmPct)", true, 70, false},
		{"61 while pending — still blocked (above RearmPct)", true, 61, false},
		{"59 while pending — re-arms but below trigger", true, 59, false},
		{"87 after rearm — fires", false, 87, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Compactor{pending: tt.pending}
			got := c.ShouldTrigger(tt.ctxPct)
			if got != tt.want {
				t.Errorf("ShouldTrigger(%d) with pending=%v = %v, want %v",
					tt.ctxPct, tt.pending, got, tt.want)
			}
		})
	}
}

func TestShouldTriggerRearms(t *testing.T) {
	c := &Compactor{}

	// First trigger.
	if !c.ShouldTrigger(90) {
		t.Fatal("expected first trigger at 90%")
	}
	// Simulate Run() setting pending.
	c.pending = true

	// Still pending — same high context, must not re-trigger.
	if c.ShouldTrigger(90) {
		t.Error("must not re-trigger while pending")
	}
	if c.ShouldTrigger(85) {
		t.Error("must not re-trigger while pending")
	}

	// Context drops below RearmPct — should re-arm but not yet trigger (pct < 85).
	if c.ShouldTrigger(59) {
		t.Error("59%% < TriggerPct, must not trigger even after rearm")
	}
	if c.pending {
		t.Error("pending should be cleared after dropping below RearmPct")
	}

	// Now climbs again — second compaction fires.
	if !c.ShouldTrigger(88) {
		t.Error("expected second trigger at 88%% after rearm")
	}
	c.pending = true

	// Second compaction: descend through 84 → 70 → 61 (all still pending),
	// then 59 re-arms, then 85 fires.
	c.pending = true
	if c.ShouldTrigger(84) {
		t.Error("84%%: should be blocked (above RearmPct)")
	}
	if !c.pending {
		t.Error("pending should still be set at 84%%")
	}
	if c.ShouldTrigger(70) {
		t.Error("70%%: should be blocked (above RearmPct)")
	}
	if !c.pending {
		t.Error("pending should still be set at 70%%")
	}
	if c.ShouldTrigger(61) {
		t.Error("61%%: should be blocked (above RearmPct)")
	}
	if !c.pending {
		t.Error("pending should still be set at 61%%")
	}
	// 59 < RearmPct: re-arms, but 59 < TriggerPct so no fire yet.
	if c.ShouldTrigger(59) {
		t.Error("59%%: re-armed but below TriggerPct, must not fire")
	}
	if c.pending {
		t.Error("pending should be cleared at 59%%")
	}
	// Now climbs to 85 — second compaction fires.
	if !c.ShouldTrigger(85) {
		t.Error("expected second compaction to fire at 85%% after 84→70→61→59 descent")
	}
}

// ── provider-view assembly tests ─────────────────────────────────────────────

// makeMsg is a test helper that builds a MessageParam with a single text block.
func makeMsg(role anthropic.MessageParamRole, text string) anthropic.MessageParam {
	return anthropic.MessageParam{
		Role: role,
		Content: []anthropic.ContentBlockParamUnion{
			anthropic.NewTextBlock(text),
		},
	}
}

// msgText extracts the first text block from a MessageParam.
func msgText(m anthropic.MessageParam) string {
	for _, blk := range m.Content {
		// Unmarshal the union into a generic map to read type + text.
		raw, err := json.Marshal(blk)
		if err != nil {
			continue
		}
		var obj struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		if obj.Type == "text" {
			return obj.Text
		}
	}
	return ""
}

// makeToolResultMsg returns a user message containing a single tool_result block.
func makeToolResultMsg(label string) anthropic.MessageParam {
	b, _ := json.Marshal(map[string]string{
		"type":        "tool_result",
		"tool_use_id": "tc_" + label,
		"content":     "result of " + label,
	})
	var u anthropic.ContentBlockParamUnion
	json.Unmarshal(b, &u)
	return anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleUser,
		Content: []anthropic.ContentBlockParamUnion{u},
	}
}

// makeToolUseAssistant returns an assistant message containing a tool_use block
// (i.e. the model has called a tool but the result has not arrived yet).
func makeToolUseAssistant(label string) anthropic.MessageParam {
	b, _ := json.Marshal(map[string]string{
		"type":  "tool_use",
		"id":    "tc_" + label,
		"name":  "bash",
		"input": `{}`,
	})
	var u anthropic.ContentBlockParamUnion
	json.Unmarshal(b, &u)
	return anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleAssistant,
		Content: []anthropic.ContentBlockParamUnion{u},
	}
}

// makeToolUseExchange returns the 4-message sequence for one tool-using turn:
// user(text) → assistant(tool_use) → user(tool_result) → assistant(text).
func makeToolUseExchange(label string) []anthropic.MessageParam {
	U := anthropic.MessageParamRoleUser
	A := anthropic.MessageParamRoleAssistant
	return []anthropic.MessageParam{
		makeMsg(U, "user asks "+label),
		makeToolUseAssistant(label),   // assistant with real tool_use block
		makeToolResultMsg(label),      // user with real tool_result block
		makeMsg(A, "assistant done "+label),
	}
}

func TestAssembleProviderView(t *testing.T) {
	U := anthropic.MessageParamRoleUser
	A := anthropic.MessageParamRoleAssistant

	// Build a predictable history: 5 pairs (10 messages).
	var history []anthropic.MessageParam
	for i := 1; i <= 5; i++ {
		history = append(history,
			makeMsg(U, fmt.Sprintf("user turn %d", i)),
			makeMsg(A, fmt.Sprintf("assistant turn %d", i)),
		)
	}

	// Build a mixed history: 2 plain turns then 1 tool-use exchange (6 msgs).
	var mixedHistory []anthropic.MessageParam
	mixedHistory = append(mixedHistory,
		makeMsg(U, "plain user 1"), makeMsg(A, "plain assistant 1"),
		makeMsg(U, "plain user 2"), makeMsg(A, "plain assistant 2"),
	)
	mixedHistory = append(mixedHistory, makeToolUseExchange("tool-A")...)

	const handoff = "TASK STATE: writing tests\nNEXT STEPS: run go test"

	tests := []struct {
		name          string
		msgs          []anthropic.MessageParam
		handoff       string
		wantPrefixLen int    // first 2 msgs: handoff pair
		wantTailPairs int    // how many exchange pairs follow
		wantLastUser  string // text of the last user message in tail
	}{
		{
			name:          "5 pairs, keep last 3",
			msgs:          history,
			handoff:       handoff,
			wantPrefixLen: 2,
			wantTailPairs: 3,
			wantLastUser:  "user turn 5",
		},
		{
			name:          "2 pairs, keep all (fewer than TailTurns)",
			msgs:          history[:4],
			handoff:       handoff,
			wantPrefixLen: 2,
			wantTailPairs: 2,
			wantLastUser:  "user turn 2",
		},
		{
			name:          "exactly 3 pairs",
			msgs:          history[:6],
			handoff:       handoff,
			wantPrefixLen: 2,
			wantTailPairs: 3,
			wantLastUser:  "user turn 3",
		},
		{
			name:          "empty history",
			msgs:          nil,
			handoff:       handoff,
			wantPrefixLen: 2,
			wantTailPairs: 0,
			wantLastUser:  "",
		},
		{
			// Second compaction: input is only the post-compaction real turns
			// (agent passes turnMsgs, not msgs). The old handoff pair must
			// never appear; the view must contain exactly one handoff block.
			name:          "second compaction — only real post-compaction turns, no stacked handoffs",
			msgs:          history[:4], // 2 real turns since last compaction
			handoff:       "TASK STATE: second pass\nNEXT STEPS: finish",
			wantPrefixLen: 2,
			wantTailPairs: 2,
			wantLastUser:  "user turn 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Compactor{pending: true, Handoff: tt.handoff}
			view := c.AssembleProviderView(tt.msgs)

			// First message must be the handoff user message.
			if len(view) < 2 {
				t.Fatalf("view too short: %d", len(view))
			}
			if view[0].Role != U {
				t.Errorf("view[0] role = %v, want user", view[0].Role)
			}
			if !strings.Contains(msgText(view[0]), tt.handoff) {
				t.Errorf("view[0] missing handoff text")
			}

			// Second message must be the acknowledgement assistant message.
			if view[1].Role != A {
				t.Errorf("view[1] role = %v, want assistant", view[1].Role)
			}
			if !strings.Contains(msgText(view[1]), "understood") {
				t.Errorf("view[1] missing ack text, got: %q", msgText(view[1]))
			}

			// Tail length check.
			tail := view[2:]
			gotPairs := len(tail) / 2
			if gotPairs != tt.wantTailPairs {
				t.Errorf("tail pairs = %d, want %d (tail len=%d)", gotPairs, tt.wantTailPairs, len(tail))
			}

			// Last user message in tail.
			if tt.wantLastUser != "" {
				lastUser := ""
				for i := len(tail) - 1; i >= 0; i-- {
					if tail[i].Role == U {
						lastUser = msgText(tail[i])
						break
					}
				}
				if lastUser != tt.wantLastUser {
					t.Errorf("last user msg = %q, want %q", lastUser, tt.wantLastUser)
				}
			}

			// Tail must alternate user/assistant and be in chronological order.
			for i, m := range tail {
				wantRole := U
				if i%2 == 1 {
					wantRole = A
				}
				if m.Role != wantRole {
					t.Errorf("tail[%d] role = %v, want %v", i, m.Role, wantRole)
				}
			}

			// Invariant: exactly one handoff block in the entire view.
			handoffCount := 0
			for _, m := range view {
				if strings.Contains(msgText(m), "[COMPACTION HANDOFF]") {
					handoffCount++
				}
			}
			if handoffCount != 1 {
				t.Errorf("want exactly 1 handoff block in view, got %d", handoffCount)
			}
		})
	}
}

// TestTailTurnsExchangeDefinition is the canonical test for tailTurns.
//
// Definition under test: an exchange is the contiguous slice from a real
// user message (isInitiatingUser) through to the nearest following isClosed
// assistant. Every case is named against this definition. The shared
// invariant checker at the bottom of the loop enforces all three API rules:
//
//  1. first message is always an initiating user message
//  2. last message is always a closed assistant (no tool_use blocks)
//  3. no tool_result appears at position 0 or without a preceding assistant
func TestTailTurnsExchangeDefinition(t *testing.T) {
	U := anthropic.MessageParamRoleUser
	A := anthropic.MessageParamRoleAssistant

	// ── named histories ───────────────────────────────────────────────────

	// plain: user(text) → assistant(text)
	plain := func(label string) []anthropic.MessageParam {
		return []anthropic.MessageParam{
			makeMsg(U, "user "+label),
			makeMsg(A, "assistant "+label),
		}
	}

	// singleTool: user(text) → assistant(tool_use) → user(tool_result) → assistant(text)
	singleTool := makeToolUseExchange // alias for clarity

	// multiTool: user(text) → assistant(tool_use) → user(tool_result)
	//                       → assistant(tool_use) → user(tool_result) → assistant(text)
	// Two tool_use/tool_result round-trips before the closing assistant.
	multiTool := func(label string) []anthropic.MessageParam {
		return []anthropic.MessageParam{
			makeMsg(U, "user "+label),
			makeToolUseAssistant(label + "-1"),
			makeToolResultMsg(label + "-1"),
			makeToolUseAssistant(label + "-2"),
			makeToolResultMsg(label + "-2"),
			makeMsg(A, "assistant "+label),
		}
	}

	tests := []struct {
		name          string
		history       []anthropic.MessageParam
		n             int
		wantMsgCount  int    // total messages expected in result
		wantFirstUser string // msgText of result[0]
		wantLastText  string // msgText of result[last] — the closing assistant
	}{
		// ── complete exchange variants ─────────────────────────────────────
		{
			name:          "plain exchange, n=1",
			history:       plain("A"),
			n:             1,
			wantMsgCount:  2,
			wantFirstUser: "user A",
			wantLastText:  "assistant A",
		},
		{
			name:          "single-tool exchange, n=1",
			history:       singleTool("T"),
			n:             1,
			wantMsgCount:  4,
			wantFirstUser: "user asks T",
			wantLastText:  "assistant done T",
		},
		{
			name: "multi-tool exchange (2 tool calls), n=1",
			history: multiTool("M"),
			n:             1,
			wantMsgCount:  6,
			wantFirstUser: "user M",
			wantLastText:  "assistant M",
		},
		{
			name: "plain then single-tool, n=1 keeps only single-tool",
			history: append(plain("A"), singleTool("T")...),
			n:             1,
			wantMsgCount:  4,
			wantFirstUser: "user asks T",
			wantLastText:  "assistant done T",
		},
		{
			name: "plain then single-tool, n=2 keeps both",
			history: append(plain("A"), singleTool("T")...),
			n:             2,
			wantMsgCount:  6,
			wantFirstUser: "user A",
			wantLastText:  "assistant done T",
		},
		{
			name: "three plain exchanges, n=3 keeps all",
			history: append(append(plain("A"), plain("B")...), plain("C")...),
			n:             3,
			wantMsgCount:  6,
			wantFirstUser: "user A",
			wantLastText:  "assistant C",
		},
		{
			name: "three plain exchanges, n=2 keeps last two",
			history: append(append(plain("A"), plain("B")...), plain("C")...),
			n:             2,
			wantMsgCount:  4,
			wantFirstUser: "user B",
			wantLastText:  "assistant C",
		},
		// ── (a) tool traffic in the middle of the tail window ────────────────
		{
			// plain + multi-tool, n=2: both exchanges kept, all 8 messages
			// including the two interleaved tool_use/tool_result pairs.
			name: "(a) multi-tool exchange inside tail window kept whole",
			history: append(plain("A"), multiTool("M")...),
			n:             2,
			wantMsgCount:  8, // 2 (plain) + 6 (multi-tool)
			wantFirstUser: "user A",
			wantLastText:  "assistant M",
		},
		// ── (b) window boundary that would land mid-multi-tool-exchange ───
		{
			// History: plain-A / multi-tool-M / plain-B (3 exchanges, 10 msgs).
			// n=2 asks for the last 2 exchanges: multi-tool-M (6 msgs) + plain-B (2 msgs).
			// The boundary between exchange 1 and exchange 2 falls inside the
			// multi-tool block; the whole block must be kept atomically.
			name: "(b) n=2 boundary falls before multi-tool exchange — kept whole",
			history: append(append(plain("A"), multiTool("M")...), plain("B")...),
			n:             2,
			wantMsgCount:  8, // 6 (multi-tool-M) + 2 (plain-B)
			wantFirstUser: "user M",
			wantLastText:  "assistant B",
		},
		// ── (c) history ending in a partially-complete multi-tool exchange ─
		{
			// Second tool_use issued, its tool_result not yet received.
			// Sequence: plain-A / user-B + tool_use-B1 + tool_result-B1 + tool_use-B2
			// The incomplete exchange (missing tool_result-B2 and closing assistant)
			// must be excluded entirely; only plain-A is returned.
			name: "(c) partial multi-tool at tail (2nd tool_use, no result) excluded",
			history: append(plain("A"),
				makeMsg(U, "user B"),
				makeToolUseAssistant("B-1"),
				makeToolResultMsg("B-1"),
				makeToolUseAssistant("B-2"), // second tool issued, result not yet received
			),
			n:             1,
			wantMsgCount:  2, // only complete plain-A
			wantFirstUser: "user A",
			wantLastText:  "assistant A",
		},
		{
			// (c) exact agent mid-tool-use moment: turnMsgs ends with
			// assistant(tool_use) because compaction fired after resp.ToParam()
			// was appended but before the tool_result was appended.
			// The agent now guards this with StopReason != ToolUse, but
			// AssembleProviderView must also be safe if called in this state:
			// the dangling tool_use assistant is excluded, prior exchange kept.
			name: "(c) agent mid-tool-use: turnMsgs ends with assistant(tool_use)",
			history: append(plain("A"),
				makeMsg(U, "user B"),
				makeToolUseAssistant("B"), // StopReason==ToolUse; tool_result not yet appended
			),
			n:             1,
			wantMsgCount:  2, // only complete plain-A; dangling tool_use excluded
			wantFirstUser: "user A",
			wantLastText:  "assistant A",
		},
		// ── incomplete tail fragments — all must be excluded ───────────────
		{
			// assistant(tool_use) with no following tool_result: the API
			// hard-rejects a conversation that ends on a tool_use assistant.
			name: "trailing assistant(tool_use) excluded — no orphaned tool_call",
			history: append(plain("A"),
				makeMsg(U, "user B"),
				makeToolUseAssistant("B"), // incomplete: no tool_result follows
			),
			n:             1,
			wantMsgCount:  2, // only the complete plain exchange
			wantFirstUser: "user A",
			wantLastText:  "assistant A",
		},
		{
			// user(tool_result) with no following closed assistant: the API
			// hard-rejects a tool_result with no matching tool_call in view.
			name: "trailing user(tool_result) excluded — no orphaned tool_result",
			history: append(plain("A"),
				makeMsg(U, "user B"),
				makeToolUseAssistant("B"),
				makeToolResultMsg("B"), // incomplete: closing assistant not yet received
			),
			n:             1,
			wantMsgCount:  2, // only the complete plain exchange
			wantFirstUser: "user A",
			wantLastText:  "assistant A",
		},
		{
			// Empty result when the only history is an incomplete exchange.
			name: "only incomplete exchange — empty result",
			history: []anthropic.MessageParam{
				makeMsg(U, "user A"),
				makeToolUseAssistant("A"), // no result
			},
			n:            1,
			wantMsgCount: 0,
		},
		// ── edge cases ────────────────────────────────────────────────────
		{
			name:         "empty history",
			history:      nil,
			n:            3,
			wantMsgCount: 0,
		},
		{
			name:         "n=0",
			history:      plain("A"),
			n:            0,
			wantMsgCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tailTurns(tt.history, tt.n)

			if len(got) != tt.wantMsgCount {
				t.Fatalf("msg count = %d, want %d", len(got), tt.wantMsgCount)
			}
			if len(got) == 0 {
				return
			}

			// Invariant 1: first message is an initiating user message.
			if !isInitiatingUser(got[0]) {
				t.Errorf("got[0] is not an initiating user message (role=%v, isToolResult=%v)",
					got[0].Role, isToolResultMsg(got[0]))
			}
			if tt.wantFirstUser != "" && msgText(got[0]) != tt.wantFirstUser {
				t.Errorf("got[0] text = %q, want %q", msgText(got[0]), tt.wantFirstUser)
			}

			// Invariant 2: last message is a closed assistant (no tool_use blocks).
			last := got[len(got)-1]
			if !isClosed(last) {
				t.Errorf("last msg is not isClosed (role=%v, types=%v)", last.Role, contentTypes(last))
			}
			if tt.wantLastText != "" && msgText(last) != tt.wantLastText {
				t.Errorf("last msg text = %q, want %q", msgText(last), tt.wantLastText)
			}

			// Invariant 3: no tool_result appears at position 0 or without a
			// preceding assistant message.
			for i, m := range got {
				if !isToolResultMsg(m) {
					continue
				}
				if i == 0 {
					t.Errorf("got[0] is a tool_result — orphaned tool_result at head")
				} else if got[i-1].Role != A {
					t.Errorf("got[%d] is tool_result but got[%d].role=%v, want assistant",
						i, i-1, got[i-1].Role)
				}
			}
		})
	}
}

// TestNoStackedHandoffs simulates the agent's two-compaction flow:
// first compaction produces a view; agent resets turnMsgs; post-compaction
// turns accumulate in turnMsgs; second compaction calls AssembleProviderView
// with turnMsgs only — the old handoff pair must not appear in the new view.
func TestNoStackedHandoffs(t *testing.T) {
	U := anthropic.MessageParamRoleUser
	A := anthropic.MessageParamRoleAssistant

	// ── first compaction ──────────────────────────────────────────────────
	c := &Compactor{pending: true, Handoff: "TASK STATE: first\nNEXT STEPS: continue"}

	// Simulate 5 real turns before first compaction.
	var allMsgs []anthropic.MessageParam
	for i := 1; i <= 5; i++ {
		allMsgs = append(allMsgs,
			makeMsg(U, fmt.Sprintf("pre-compact user %d", i)),
			makeMsg(A, fmt.Sprintf("pre-compact assistant %d", i)),
		)
	}

	// Agent calls AssembleProviderView(turnMsgs=allMsgs), then resets turnMsgs.
	firstView := c.AssembleProviderView(allMsgs)
	turnMsgs := []anthropic.MessageParam(nil) // agent resets after compaction

	// firstView = [handoff-user, ack-assistant, last-3-pairs-of-allMsgs]
	if len(firstView) != 8 { // 2 + 3*2
		t.Fatalf("first view length = %d, want 8", len(firstView))
	}

	// ── post-compaction turns accumulate in turnMsgs ──────────────────────
	for i := 1; i <= 2; i++ {
		u := makeMsg(U, fmt.Sprintf("post-compact user %d", i))
		a := makeMsg(A, fmt.Sprintf("post-compact assistant %d", i))
		turnMsgs = append(turnMsgs, u, a)
		// msgs (provider view) would also grow, but we only care about turnMsgs here.
	}

	// ── second compaction ─────────────────────────────────────────────────
	c.Handoff = "TASK STATE: second\nNEXT STEPS: finish"
	// Agent passes turnMsgs (post-compaction real turns), not the full msgs.
	secondView := c.AssembleProviderView(turnMsgs)

	// Must contain exactly one handoff block — the new one.
	handoffCount := 0
	for _, m := range secondView {
		if strings.Contains(msgText(m), "[COMPACTION HANDOFF]") {
			handoffCount++
		}
	}
	if handoffCount != 1 {
		t.Errorf("second view: want 1 handoff block, got %d", handoffCount)
	}

	// The new handoff must reference the second handoff text, not the first.
	if !strings.Contains(msgText(secondView[0]), "TASK STATE: second") {
		t.Errorf("second view handoff contains wrong text: %q", msgText(secondView[0]))
	}
	if strings.Contains(msgText(secondView[0]), "TASK STATE: first") {
		t.Errorf("second view handoff still contains first handoff text")
	}

	// Tail must be the post-compaction turns only.
	tail := secondView[2:]
	for _, m := range tail {
		text := msgText(m)
		if strings.Contains(text, "pre-compact") {
			t.Errorf("pre-compaction turn leaked into second view tail: %q", text)
		}
		if strings.Contains(text, "[COMPACTION HANDOFF]") {
			t.Errorf("old handoff pair leaked into second view tail: %q", text)
		}
	}

	// Tail must contain only the 2 post-compaction pairs.
	if len(tail) != 4 { // 2 pairs * 2
		t.Errorf("tail length = %d, want 4 (2 post-compaction pairs)", len(tail))
	}
	if msgText(tail[len(tail)-2]) != "post-compact user 2" {
		t.Errorf("last tail user = %q, want %q", msgText(tail[len(tail)-2]), "post-compact user 2")
	}

	// Structure: view = [new-handoff-user, ack, post-compact-u1, post-compact-a1, post-compact-u2, post-compact-a2]
	wantRoles := []anthropic.MessageParamRole{U, A, U, A, U, A}
	for i, want := range wantRoles {
		if secondView[i].Role != want {
			t.Errorf("secondView[%d].Role = %v, want %v", i, secondView[i].Role, want)
		}
	}
}

// ── Run() integration with stub provider ─────────────────────────────────────

type stubProvider struct {
	replyText string
}

func (s *stubProvider) Complete(
	_ context.Context, _ string,
	_ []anthropic.TextBlockParam,
	_ []anthropic.MessageParam,
	_ []anthropic.ToolUnionParam, _ int64,
) (*anthropic.Message, error) {
	// ContentBlockUnion is the concrete type in Message.Content.
	// Its exported fields (Type, Text) can be set directly.
	blk := anthropic.ContentBlockUnion{Type: "text", Text: s.replyText}
	return &anthropic.Message{Content: []anthropic.ContentBlockUnion{blk}}, nil
}

func (s *stubProvider) ContextWindow(_ string) int64 { return 200_000 }

var _ provider.Provider = (*stubProvider)(nil)

func TestRunStoresHandoff(t *testing.T) {
	want := "TASK STATE: all good\nNEXT STEPS: ship it"
	stub := &stubProvider{replyText: want}
	c := &Compactor{}

	err := c.Run(context.Background(), stub, "model", nil, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !c.pending {
		t.Error("pending should be true after Run")
	}
	if c.Handoff != want {
		t.Errorf("Handoff = %q, want %q", c.Handoff, want)
	}
}
