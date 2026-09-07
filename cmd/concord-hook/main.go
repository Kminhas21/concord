// Command concord-hook is the thin client Claude Code invokes as a hook. Each
// subcommand reads hook JSON on stdin, makes one RPC to the concord daemon, and
// sets its exit code. Correctness hooks (pre-tool-use) fail open when the daemon
// is unreachable so a crashed daemon never bricks editing; advisory hooks always
// exit 0.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/hashing"
	"github.com/Kminhas21/concord/internal/hook"
)

const defaultAddr = "127.0.0.1:8973"

func daemonURL() string {
	addr := os.Getenv("CONCORD_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	return "http://" + addr
}

func newClient() concordv1connect.CoordinationServiceClient {
	return concordv1connect.NewCoordinationServiceClient(http.DefaultClient, daemonURL())
}

// normalize resolves a path to an absolute, cleaned, forward-slash form so that
// keys agree regardless of the caller: edit tools pass absolute file_path,
// while `git status --porcelain` yields repo-relative paths (resolved against
// the hook's working directory, which is the repo root).
func normalize(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	return filepath.ToSlash(filepath.Clean(abs))
}

func readInput() (hook.Input, error) {
	var in hook.Input
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return in, err
	}
	if len(data) == 0 {
		return in, nil
	}
	err = json.Unmarshal(data, &in)
	return in, err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: concord-hook <pre-tool-use|post-tool-use|subagent-start>")
		os.Exit(2)
	}
	in, err := readInput()
	if err != nil {
		// Never block on our own parse failure.
		fmt.Fprintln(os.Stderr, "concord-hook: reading input:", err)
		os.Exit(0)
	}

	ctx := context.Background()
	switch os.Args[1] {
	case "pre-tool-use":
		preToolUse(ctx, in)
	case "post-tool-use":
		postToolUse(ctx, in)
	case "subagent-start":
		subagentStart(ctx, in)
	default:
		fmt.Fprintln(os.Stderr, "concord-hook: unknown subcommand:", os.Args[1])
		os.Exit(0)
	}
}

// preToolUse runs the version check on edit tools. It exits 2 to block a stale
// edit, and fails open (exit 0) if the daemon is unreachable.
func preToolUse(ctx context.Context, in hook.Input) {
	if !hook.IsEditTool(in.ToolName) || in.EditPath() == "" {
		os.Exit(0)
	}
	path := normalize(in.EditPath())
	currentHash, _, err := hashing.HashFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "concord-hook: hashing", path, ":", err)
		os.Exit(0)
	}
	resp, err := newClient().CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId:     in.ActorID(),
		Path:        path,
		CurrentHash: currentHash,
	}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "concord: version check unavailable, allowing edit:", err)
		os.Exit(0)
	}
	if !resp.Msg.GetAllowed() {
		fmt.Fprintln(os.Stderr, resp.Msg.GetMessage())
		os.Exit(2)
	}
	os.Exit(0)
}

// postToolUse records footprint and advances read-hashes. It is advisory and
// always exits 0.
func postToolUse(ctx context.Context, in hook.Input) {
	c := newClient()
	actor := in.ActorID()
	switch {
	case in.ToolName == "Read":
		p := normalize(in.ToolInput.FilePath)
		recordRead(ctx, c, actor, p)
		// Exploration dedup: opt in with CONCORD_RECORD_READS to also add reads
		// to the actual footprint (SPEC user story 16).
		if hook.RecordReads(os.Getenv("CONCORD_RECORD_READS")) {
			appendActual(ctx, c, actor, p)
		}
	case hook.IsEditTool(in.ToolName):
		// Advance the actor's own read-hash to the post-edit content so it is not
		// blocked on its own change, and record the write in the footprint.
		np := normalize(in.EditPath())
		recordRead(ctx, c, actor, np)
		appendActual(ctx, c, actor, np)
	case hook.IsShellTool(in.ToolName):
		reconcileShellWrites(ctx, c, actor)
	}
	os.Exit(0)
}

// subagentStart registers the predicted footprint from the delegation prompt.
// Best-effort; always exits 0.
func subagentStart(ctx context.Context, in hook.Input) {
	if in.Prompt != "" {
		_, _ = newClient().RegisterPredicted(ctx, connect.NewRequest(&concordv1.RegisterPredictedRequest{
			ActorId:        in.ActorID(),
			IntentText:     in.Prompt,
			PredictedPaths: hook.ExtractPredictedPaths(in.Prompt),
		}))
	}
	os.Exit(0)
}

func recordRead(ctx context.Context, c concordv1connect.CoordinationServiceClient, actor, path string) {
	if path == "" {
		return
	}
	h, exists, err := hashing.HashFile(path)
	if err != nil || !exists {
		return
	}
	_, _ = c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{
		ActorId: actor, Path: path, Hash: h,
	}))
}

func appendActual(ctx context.Context, c concordv1connect.CoordinationServiceClient, actor, path string) {
	if path == "" {
		return
	}
	_, _ = c.AppendActual(ctx, connect.NewRequest(&concordv1.AppendActualRequest{
		ActorId: actor, Path: path,
	}))
}

// reconcileShellWrites finds what a shell command just wrote (via git) and
// advances the actor's own read-hash for each, so it is not blocked on its own
// out-of-band edits. Silently does nothing outside a git working tree.
func reconcileShellWrites(ctx context.Context, c concordv1connect.CoordinationServiceClient, actor string) {
	out, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
	if err != nil {
		return
	}
	for _, raw := range hook.ParseGitStatusPorcelain(string(out)) {
		path := normalize(raw)
		h, exists, err := hashing.HashFile(path)
		if err != nil || !exists {
			continue
		}
		_, _ = c.ReconcileFileChange(ctx, connect.NewRequest(&concordv1.ReconcileFileChangeRequest{
			ActorId: actor, Path: path, NewHash: h,
		}))
		appendActual(ctx, c, actor, path)
	}
}
