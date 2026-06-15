package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aliirz/raqim/internal/permission"
	"github.com/aliirz/raqim/internal/provider"
	"github.com/aliirz/raqim/internal/session"
	"github.com/aliirz/raqim/internal/tools"
)

// ── stub provider ────────────────────────────────────────────────────────────

type stubProv struct {
	seq []*anthropic.Message
	i   int
}

func (s *stubProv) Complete(
	_ context.Context, _ string,
	_ []anthropic.TextBlockParam,
	_ []anthropic.MessageParam,
	_ []anthropic.ToolUnionParam, _ int64,
) (*anthropic.Message, error) {
	if s.i >= len(s.seq) {
		return &anthropic.Message{StopReason: anthropic.StopReasonEndTurn}, nil
	}
	r := s.seq[s.i]
	s.i++
	return r, nil
}

func (s *stubProv) ContextWindow(_ string) int64 { return 200_000 }

var _ provider.Provider = (*stubProv)(nil)

// ── message builders ─────────────────────────────────────────────────────────

func textBlock(text string) anthropic.ContentBlockUnion {
	return anthropic.ContentBlockUnion{Type: "text", Text: text}
}

// toolUseBlock builds a ContentBlockUnion with type=="tool_use" via JSON
// round-trip so that AsAny() returns a fully populated ToolUseBlock including
// the JSON.Input metadata field the agent reads.
func toolUseBlock(id, name string, input json.RawMessage) anthropic.ContentBlockUnion {
	raw, _ := json.Marshal(map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": input,
	})
	var blk anthropic.ContentBlockUnion
	json.Unmarshal(raw, &blk)
	return blk
}

func maxTokensMsg(blocks ...anthropic.ContentBlockUnion) *anthropic.Message {
	return &anthropic.Message{
		StopReason: anthropic.StopReasonMaxTokens,
		Content:    blocks,
		Usage:      anthropic.Usage{InputTokens: 100, OutputTokens: 16384},
	}
}

func toolUseMsg(blocks ...anthropic.ContentBlockUnion) *anthropic.Message {
	return &anthropic.Message{
		StopReason: anthropic.StopReasonToolUse,
		Content:    blocks,
		Usage:      anthropic.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

func endTurnMsg(text string) *anthropic.Message {
	return &anthropic.Message{
		StopReason: anthropic.StopReasonEndTurn,
		Content:    []anthropic.ContentBlockUnion{textBlock(text)},
		Usage:      anthropic.Usage{InputTokens: 100, OutputTokens: 10},
	}
}

// ── test agent factory ───────────────────────────────────────────────────────

func newTestAgent(t *testing.T, prov provider.Provider, permIn io.Reader) *Agent {
	t.Helper()
	dir := t.TempDir()
	sess, err := session.NewInDir(dir, "test", "stub")
	if err != nil {
		t.Fatalf("session.NewInDir: %v", err)
	}
	t.Cleanup(func() { sess.End("test") })

	cwd := filepath.Join(dir, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	raqimDir := filepath.Join(dir, ".raqim")

	var permReader io.Reader = strings.NewReader("a\na\na\na\na\n") // default: allow-all
	if permIn != nil {
		permReader = permIn
	}
	perm := permission.New(bufio.NewReader(permReader), io.Discard, cwd, raqimDir)

	var out bytes.Buffer
	a := &Agent{
		Prov:    prov,
		Model:   "stub",
		Perm:    perm,
		Sess:    sess,
		ToolCtx: &tools.Ctx{Cwd: cwd, MemoryPath: dir, Project: "test", Touched: map[string]bool{}},
		Out:     &out,
	}
	a.Init("")
	return a
}

// ── invariant checker ────────────────────────────────────────────────────────

// checkMsgsValid asserts that every assistant message containing a tool_use
// block is immediately followed by a user message containing a tool_result
// block — the API invariant that prevents 400 errors.
func checkMsgsValid(t *testing.T, msgs []anthropic.MessageParam) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != anthropic.MessageParamRoleAssistant {
			continue
		}
		hasToolUse := false
		for _, blk := range m.Content {
			if blk.OfToolUse != nil {
				hasToolUse = true
				break
			}
		}
		if !hasToolUse {
			continue
		}
		if i+1 >= len(msgs) {
			t.Errorf("msgs[%d]: assistant with tool_use is the last message (no tool_result follows)", i)
			continue
		}
		next := msgs[i+1]
		if next.Role != anthropic.MessageParamRoleUser {
			t.Errorf("msgs[%d]: assistant with tool_use, msgs[%d].Role=%v want user", i, i+1, next.Role)
			continue
		}
		hasToolResult := false
		for _, blk := range next.Content {
			if blk.OfToolResult != nil {
				hasToolResult = true
				break
			}
		}
		if !hasToolResult {
			t.Errorf("msgs[%d]: user message following tool_use has no tool_result block", i+1)
		}
	}
}

// toolResultText extracts the text from the first tool_result block in a
// user message's content, or "" if none is present.
func toolResultText(msg anthropic.MessageParam) string {
	for _, blk := range msg.Content {
		if blk.OfToolResult == nil {
			continue
		}
		for _, c := range blk.OfToolResult.Content {
			if c.OfText != nil {
				return c.OfText.Text
			}
		}
	}
	return ""
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestMaxTokensWithToolUse is the primary regression test for the wedged-session
// bug: a response truncated at max_tokens containing a tool_use must leave
// a.msgs API-valid (tool_use immediately followed by tool_result) and return
// ErrMaxTokens.
//
// Fixture: the real-world failure — write tool_use "toolu_01NmSR" with empty
// args, preceded by a large text block, StopReason max_tokens.
func TestMaxTokensWithToolUse(t *testing.T) {
	prov := &stubProv{seq: []*anthropic.Message{
		maxTokensMsg(
			textBlock("here is a very long explanation that consumed most of the token budget"),
			toolUseBlock("toolu_01NmSR", "write", json.RawMessage(`{}`)),
		),
	}}
	a := newTestAgent(t, prov, nil)

	err := a.Turn(context.Background(), "hello")

	if !errors.Is(err, ErrMaxTokens) {
		t.Fatalf("Turn() error = %v, want ErrMaxTokens", err)
	}

	// msgs must be API-valid: [user, assistant(text+tool_use), user(tool_result)]
	if len(a.msgs) != 3 {
		t.Fatalf("len(a.msgs) = %d, want 3", len(a.msgs))
	}
	checkMsgsValid(t, a.msgs)

	// Synthetic tool_result must carry guidance text so the model can self-correct.
	got := toolResultText(a.msgs[2])
	if !strings.Contains(got, "output token limit") {
		t.Errorf("synthetic tool_result missing guidance: %q", got)
	}
	if !strings.Contains(got, "did NOT run") {
		t.Errorf("synthetic tool_result missing 'did NOT run': %q", got)
	}
	if !strings.Contains(got, "split the work") {
		t.Errorf("synthetic tool_result missing 'split the work': %q", got)
	}
}

// TestMaxTokensTextOnly verifies that a max_tokens response with only a text
// block (no tool_use) returns ErrMaxTokens and leaves a.msgs valid with no
// dangling tool_use.
func TestMaxTokensTextOnly(t *testing.T) {
	prov := &stubProv{seq: []*anthropic.Message{
		maxTokensMsg(textBlock("partial output cut off here")),
	}}
	a := newTestAgent(t, prov, nil)

	err := a.Turn(context.Background(), "hello")

	if !errors.Is(err, ErrMaxTokens) {
		t.Fatalf("Turn() error = %v, want ErrMaxTokens", err)
	}
	// msgs: [user, assistant(text)] — no tool_use so no tool_result needed
	if len(a.msgs) != 2 {
		t.Fatalf("len(a.msgs) = %d, want 2", len(a.msgs))
	}
	checkMsgsValid(t, a.msgs)
}

// TestMaxTokensMultiToolUse verifies that when the truncated response contains
// multiple tool_use blocks, each gets a synthetic tool_result.
func TestMaxTokensMultiToolUse(t *testing.T) {
	prov := &stubProv{seq: []*anthropic.Message{
		maxTokensMsg(
			toolUseBlock("tu-1", "read", json.RawMessage(`{"path":"a.go"}`)),
			toolUseBlock("tu-2", "write", json.RawMessage(`{}`)),
		),
	}}
	a := newTestAgent(t, prov, nil)

	err := a.Turn(context.Background(), "hello")

	if !errors.Is(err, ErrMaxTokens) {
		t.Fatalf("Turn() error = %v, want ErrMaxTokens", err)
	}
	// msgs: [user, assistant(2×tool_use), user(2×tool_result)]
	if len(a.msgs) != 3 {
		t.Fatalf("len(a.msgs) = %d, want 3", len(a.msgs))
	}
	checkMsgsValid(t, a.msgs)

	// Both tool_results must be in the third message.
	toolResults := 0
	for _, blk := range a.msgs[2].Content {
		if blk.OfToolResult != nil {
			toolResults++
		}
	}
	if toolResults != 2 {
		t.Errorf("want 2 tool_result blocks in msgs[2], got %d", toolResults)
	}
}

// TestToolErrorStillEmitsResult verifies that a tool returning ok=false
// (e.g. file not found) still appends a tool_result to a.msgs.
func TestToolErrorStillEmitsResult(t *testing.T) {
	prov := &stubProv{seq: []*anthropic.Message{
		// First: tool_use requesting read of a non-existent file.
		toolUseMsg(toolUseBlock("tu-err", "read", json.RawMessage(`{"path":"/does/not/exist.go"}`))),
		// Second: end_turn after the model sees the error result.
		endTurnMsg("understood"),
	}}
	a := newTestAgent(t, prov, nil)

	err := a.Turn(context.Background(), "read that file")

	if err != nil {
		t.Fatalf("Turn() error = %v, want nil", err)
	}
	// msgs: [user, assistant(tool_use), user(tool_result), assistant(text)]
	if len(a.msgs) != 4 {
		t.Fatalf("len(a.msgs) = %d, want 4", len(a.msgs))
	}
	checkMsgsValid(t, a.msgs)
}

// TestToolEmptyOutputStillEmitsResult verifies that a tool returning "" as
// output still appends a tool_result to a.msgs.
func TestToolEmptyOutputStillEmitsResult(t *testing.T) {
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty.txt")
	os.WriteFile(emptyFile, []byte(""), 0o644)

	prov := &stubProv{seq: []*anthropic.Message{
		toolUseMsg(toolUseBlock("tu-empty", "read", json.RawMessage(
			`{"path":"`+emptyFile+`"}`,
		))),
		endTurnMsg("ok empty"),
	}}
	a := newTestAgent(t, prov, nil)

	err := a.Turn(context.Background(), "read it")

	if err != nil {
		t.Fatalf("Turn() error = %v, want nil", err)
	}
	checkMsgsValid(t, a.msgs)
	if len(a.msgs) != 4 {
		t.Fatalf("len(a.msgs) = %d, want 4", len(a.msgs))
	}
}

// TestPermissionDeniedStillEmitsResult verifies that a permission-denied tool
// still appends a tool_result so a.msgs stays API-valid.
func TestPermissionDeniedStillEmitsResult(t *testing.T) {
	// bash needs permission; reader answers "n" (deny) then "" (no feedback).
	permReader := strings.NewReader("n\n\n")

	prov := &stubProv{seq: []*anthropic.Message{
		toolUseMsg(toolUseBlock("tu-deny", "bash", json.RawMessage(`{"command":"echo hi"}`))),
		endTurnMsg("ok denied"),
	}}
	a := newTestAgent(t, prov, permReader)

	err := a.Turn(context.Background(), "run it")

	if err != nil {
		t.Fatalf("Turn() error = %v, want nil", err)
	}
	// msgs: [user, assistant(tool_use), user(tool_result with denied msg), assistant(text)]
	if len(a.msgs) != 4 {
		t.Fatalf("len(a.msgs) = %d, want 4", len(a.msgs))
	}
	checkMsgsValid(t, a.msgs)
	got := toolResultText(a.msgs[2])
	if !strings.Contains(got, "denied") {
		t.Errorf("tool_result should contain 'denied', got: %q", got)
	}
}

// TestMaxTokensDefaultIs16384 verifies that an Agent with MaxTokens==0 uses
// 16384, and that MaxTokens is forwarded to the provider.
func TestMaxTokensDefaultIs16384(t *testing.T) {
	var captured int64
	prov := &capturingProv{inner: &stubProv{seq: []*anthropic.Message{endTurnMsg("done")}}, got: &captured}
	a := newTestAgent(t, prov, nil)
	// MaxTokens == 0 → default

	a.Turn(context.Background(), "hi")

	if captured != 16384 {
		t.Errorf("maxTokens passed to provider = %d, want 16384", captured)
	}
}

// TestMaxTokensFieldOverride verifies that setting MaxTokens on the Agent
// overrides the default.
func TestMaxTokensFieldOverride(t *testing.T) {
	var captured int64
	prov := &capturingProv{inner: &stubProv{seq: []*anthropic.Message{endTurnMsg("done")}}, got: &captured}
	a := newTestAgent(t, prov, nil)
	a.MaxTokens = 32768

	a.Turn(context.Background(), "hi")

	if captured != 32768 {
		t.Errorf("maxTokens passed to provider = %d, want 32768", captured)
	}
}

// capturingProv wraps a Provider and records the last maxTokens argument.
type capturingProv struct {
	inner provider.Provider
	got   *int64
}

func (c *capturingProv) Complete(
	ctx context.Context, model string,
	system []anthropic.TextBlockParam,
	msgs []anthropic.MessageParam,
	ts []anthropic.ToolUnionParam, maxTokens int64,
) (*anthropic.Message, error) {
	*c.got = maxTokens
	return c.inner.Complete(ctx, model, system, msgs, ts, maxTokens)
}

func (c *capturingProv) ContextWindow(model string) int64 { return c.inner.ContextWindow(model) }
