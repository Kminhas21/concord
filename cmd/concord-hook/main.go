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
	"runtime"
	"strings"
	"sync"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/hashing"
	"github.com/Kminhas21/concord/internal/hook"
	"github.com/Kminhas21/concord/internal/rpcaddr"
)

func daemonURL() string {
	addr := os.Getenv("CONCORD_ADDR")
	if addr == "" {
		addr = rpcaddr.Default
	}
	return "http://" + addr
}

func newClient() concordv1connect.CoordinationServiceClient {
	return concordv1connect.NewCoordinationServiceClient(http.DefaultClient, daemonURL())
}

var (
	rootOnce sync.Once
	rootVal  string
)

// repoRoot returns the git working-tree root of the hook's working directory,
// or "" when not in a git repo. Memoized for the process.
func repoRoot() string {
	rootOnce.Do(func() {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err == nil {
			rootVal = strings.TrimSpace(string(out))
		}
	})
	return rootVal
}

// caseInsensitiveFS is true on filesystems where paths differing only in case
// name the same file (Windows, macOS).
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// canonical is the daemon key for a path: repo-relative, forward-slash, and
// case-folded on case-insensitive filesystems, so every ingestion point (edit
// tools' absolute file_path, git status' root-relative paths) agrees on one key
// for one file.
func canonical(p string) string {
	return hook.FoldCase(hook.RepoRelative(repoRoot(), p), caseInsensitiveFS)
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

// preToolUse runs the version check on edit tools, and on shell tools snapshots
// the working tree's dirty-file hashes so the PostToolUse sweep can attribute
// the command's own writes. It exits 2 to block a stale edit, and fails open
// (exit 0) if the daemon is unreachable.
func preToolUse(ctx context.Context, in hook.Input) {
	if hook.IsShellTool(in.ToolName) {
		snapshotDirtyState(ctx, in.ActorID())
		os.Exit(0)
	}
	raw := in.EditPath()
	if !hook.IsEditTool(in.ToolName) || raw == "" {
		os.Exit(0)
	}
	currentHash, _, err := hashing.HashFile(raw) // hash the real file on disk
	if err != nil {
		fmt.Fprintln(os.Stderr, "concord-hook: hashing", raw, ":", err)
		os.Exit(0)
	}
	resp, err := newClient().CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId:     in.ActorID(),
		Path:        canonical(raw),
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
		raw := in.ToolInput.FilePath
		key := canonical(raw)
		recordRead(ctx, c, actor, key, raw)
		// Exploration dedup: opt in with CONCORD_RECORD_READS to also add reads
		// to the actual footprint (SPEC user story 16).
		if hook.RecordReads(os.Getenv("CONCORD_RECORD_READS")) {
			appendActual(ctx, c, actor, key)
		}
	case hook.IsEditTool(in.ToolName):
		// Advance the actor's own read-hash to the post-edit content so it is not
		// blocked on its own change, and record the write in the footprint.
		raw := in.EditPath()
		key := canonical(raw)
		recordRead(ctx, c, actor, key, raw)
		appendActual(ctx, c, actor, key)
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

// recordRead hashes the file at fsPath and records it under the daemon key
// (which is the canonical, repo-relative form of the same path).
func recordRead(ctx context.Context, c concordv1connect.CoordinationServiceClient, actor, key, fsPath string) {
	if key == "" {
		return
	}
	h, exists, err := hashing.HashFile(fsPath)
	if err != nil || !exists {
		return
	}
	_, _ = c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{
		ActorId: actor, Path: key, Hash: h,
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

// snapshotPath is the temp-file holding an actor's before-command dirty-file
// hashes, between its PreToolUse and PostToolUse shell hooks.
func snapshotPath(actor string) string {
	return filepath.Join(os.TempDir(), hook.SnapshotName(actor))
}

// dirtyHashes hashes every file git reports dirty in the working tree, keyed by
// the canonical daemon path. ok is false only when git status itself failed; a
// clean tree is a valid empty map (distinct from failure, which must not be
// mistaken for "nothing was dirty before").
func dirtyHashes(ctx context.Context, root string) (map[string]string, bool) {
	// core.quotepath=false keeps non-ASCII paths unescaped; ParseGitStatusPorcelain
	// still decodes any path git quotes for other reasons (e.g. a space).
	out, err := exec.CommandContext(ctx, "git", "-C", root, "-c", "core.quotepath=false", "status", "--porcelain").Output()
	if err != nil {
		return nil, false
	}
	m := make(map[string]string)
	for _, rel := range hook.ParseGitStatusPorcelain(string(out)) {
		h, exists, err := hashing.HashFile(filepath.Join(root, rel))
		if err != nil || !exists {
			continue
		}
		m[canonical(rel)] = h
	}
	return m, true
}

// snapshotDirtyState records the working tree's dirty-file hashes just before a
// shell command runs. On git failure it writes nothing, so the post-command
// sweep, finding no snapshot, reconciles nothing — correctness before the
// convenience of avoiding a false self-block.
func snapshotDirtyState(ctx context.Context, actor string) {
	root := repoRoot()
	if root == "" {
		return // not a git repo: the reconcile sweep is a no-op anyway (accepted gap)
	}
	before, ok := dirtyHashes(ctx, root)
	if !ok {
		return
	}
	b, err := json.Marshal(before)
	if err != nil {
		return
	}
	_ = os.WriteFile(snapshotPath(actor), b, 0o600)
}

// readSnapshot loads an actor's before-command dirty-file hashes. ok is false
// when no snapshot exists (the PreToolUse shell hook did not run, or git failed
// then) or it is unreadable.
func readSnapshot(path string) (map[string]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	m := make(map[string]string)
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

// reconcileShellWrites advances the actor's own read-hash for exactly the files
// its shell command wrote, attributed by diffing the before-command snapshot
// against the tree now (hook.ReconcileTargets). A file a human's editor dirtied
// out of band, and the command left untouched, has an unchanged hash and is not
// reconciled, so the actor cannot silently clobber it (US3). Without a snapshot
// it reconciles nothing. Silently does nothing outside a git working tree.
func reconcileShellWrites(ctx context.Context, c concordv1connect.CoordinationServiceClient, actor string) {
	root := repoRoot()
	if root == "" {
		return // not a git repo: nothing to reconcile (accepted gap)
	}
	snap := snapshotPath(actor)
	before, ok := readSnapshot(snap)
	if !ok {
		return // no before-state: cannot attribute the command's writes, so reconcile nothing
	}
	_ = os.Remove(snap) // one snapshot per command
	after, ok := dirtyHashes(ctx, root)
	if !ok {
		return
	}
	for _, key := range hook.ReconcileTargets(before, after) {
		_, _ = c.ReconcileFileChange(ctx, connect.NewRequest(&concordv1.ReconcileFileChangeRequest{
			ActorId: actor, Path: key, NewHash: after[key],
		}))
		appendActual(ctx, c, actor, key)
	}
}
