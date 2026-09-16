package hook_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Kminhas21/concord/internal/hook"
)

func TestActorIDPrefersAgentID(t *testing.T) {
	if got := (hook.Input{AgentID: "agent-7", SessionID: "sess-1"}).ActorID(); got != "agent-7" {
		t.Fatalf("ActorID = %q, want agent-7", got)
	}
	if got := (hook.Input{SessionID: "sess-1"}).ActorID(); got != "sess-1" {
		t.Fatalf("ActorID = %q, want sess-1 (fallback)", got)
	}
}

func TestInputParsesToolFields(t *testing.T) {
	raw := `{"session_id":"s","agent_id":"a","tool_name":"Edit","tool_input":{"file_path":"src/x.go"}}`
	var in hook.Input
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if in.ToolName != "Edit" || in.ToolInput.FilePath != "src/x.go" || in.ActorID() != "a" {
		t.Fatalf("parsed unexpectedly: %+v", in)
	}
}

func TestEditPathFallsBackToNotebookPath(t *testing.T) {
	// Most edit tools use file_path.
	if got := (hook.Input{ToolInput: hook.ToolInput{FilePath: "src/a.go"}}).EditPath(); got != "src/a.go" {
		t.Fatalf("EditPath (file_path) = %q, want src/a.go", got)
	}
	// NotebookEdit passes notebook_path instead of file_path.
	var nb hook.Input
	if err := json.Unmarshal([]byte(`{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"nb.ipynb"}}`), &nb); err != nil {
		t.Fatal(err)
	}
	if got := nb.EditPath(); got != "nb.ipynb" {
		t.Fatalf("EditPath (notebook_path) = %q, want nb.ipynb — NotebookEdit would bypass the version check", got)
	}
}

func TestToolClassification(t *testing.T) {
	if !hook.IsEditTool("Write") || hook.IsEditTool("Read") {
		t.Fatal("edit-tool classification wrong")
	}
	if !hook.IsShellTool("Bash") || !hook.IsShellTool("PowerShell") || hook.IsShellTool("Edit") {
		t.Fatal("shell-tool classification wrong")
	}
}

func TestRepoRelative(t *testing.T) {
	cases := []struct{ root, in, want string }{
		{"/repo", "/repo/src/a.go", "src/a.go"},
		{`C:\repo`, `C:\repo\src\a.go`, "src/a.go"},   // backslashes normalized
		{"/repo", "/other/x.go", "/other/x.go"},       // outside root: unchanged
		{"", "/abs/x.go", "/abs/x.go"},                // no root: unchanged
		{"/repo", "already/rel.go", "already/rel.go"}, // already relative: unchanged
		{"/repo", "/repo", "."},
	}
	for _, c := range cases {
		if got := hook.RepoRelative(c.root, c.in); got != c.want {
			t.Errorf("RepoRelative(%q,%q) = %q, want %q", c.root, c.in, got, c.want)
		}
	}
}

func TestFoldCase(t *testing.T) {
	if got := hook.FoldCase("Src/A.go", true); got != "src/a.go" {
		t.Errorf("FoldCase(insensitive) = %q, want src/a.go", got)
	}
	if got := hook.FoldCase("Src/A.go", false); got != "Src/A.go" {
		t.Errorf("FoldCase(sensitive) = %q, want Src/A.go", got)
	}
}

func TestRecordReads(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		if !hook.RecordReads(v) {
			t.Fatalf("RecordReads(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if hook.RecordReads(v) {
			t.Fatalf("RecordReads(%q) = true, want false", v)
		}
	}
}

func TestExtractPredictedPaths(t *testing.T) {
	prompt := "Refactor src/auth/login.go and update pkg/db/schema.go; see e.g. the notes. Also touch src/auth/login.go again."
	got := hook.ExtractPredictedPaths(prompt)
	want := []string{"src/auth/login.go", "pkg/db/schema.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractPredictedPaths = %v, want %v", got, want)
	}
}

func TestReconcileTargets(t *testing.T) {
	// before = dirty-file hashes snapshotted just before the shell command;
	// after = dirty-file hashes just after. A path is a reconcile target iff the
	// command actually wrote it: its content appeared or changed during the
	// command. A file already dirty and left untouched (a human's out-of-band
	// edit the command did not touch) must NOT be reconciled — that is the US3
	// protection.
	before := map[string]string{
		"src/victim.go":    "human2", // human dirtied this before the command; command leaves it
		"src/rewritten.go": "old3",   // dirty before; the command rewrites it
	}
	after := map[string]string{
		"src/victim.go":    "human2", // unchanged by the command -> must be excluded
		"src/rewritten.go": "new4",   // content changed -> reconcile
		"src/created.go":   "fresh5", // new since the snapshot -> the command wrote it -> reconcile
	}
	got := hook.ReconcileTargets(before, after)
	want := []string{"src/created.go", "src/rewritten.go"} // sorted, victim excluded
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReconcileTargets = %v, want %v", got, want)
	}

	// A file dirty before but clean after (command reverted it, or git no longer
	// lists it) is absent from `after` and must not be reconciled.
	if got := hook.ReconcileTargets(map[string]string{"a.go": "h1"}, map[string]string{}); len(got) != 0 {
		t.Fatalf("ReconcileTargets over an empty after = %v, want none", got)
	}
}

func TestSnapshotName(t *testing.T) {
	a := hook.SnapshotName("agent-7")
	// Stable for the same actor, distinct per actor, and a filesystem-safe base
	// name (no separators) so it lives cleanly in the temp dir.
	if a != hook.SnapshotName("agent-7") {
		t.Fatal("SnapshotName is not stable for one actor")
	}
	if a == hook.SnapshotName("agent-8") {
		t.Fatal("SnapshotName collides across actors")
	}
	if strings.ContainsAny(a, `/\`) {
		t.Fatalf("SnapshotName %q contains a path separator", a)
	}
	// An actor id with path-hostile characters still yields a safe name.
	if got := hook.SnapshotName("sess/../..\\x:y"); strings.ContainsAny(got, `/\:`) {
		t.Fatalf("SnapshotName did not sanitize hostile actor id: %q", got)
	}
}

func TestParseGitStatusPorcelain(t *testing.T) {
	out := " M cmd/concord/main.go\n?? new/file.go\nR  old/name.go -> new/name.go\n"
	got := hook.ParseGitStatusPorcelain(out)
	want := []string{"cmd/concord/main.go", "new/file.go", "new/name.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGitStatusPorcelain = %v, want %v", got, want)
	}
}

func TestParseGitStatusPorcelainUnquotes(t *testing.T) {
	// git quotes paths with unusual bytes (C-style): non-ASCII is octal-escaped
	// under the default core.quotepath, and spaces force quoting regardless. The
	// decoded path must equal the real on-disk name, or the reconcile key never
	// matches what the edit tool sent.
	out := "" +
		"?? \"caf\\303\\251.go\"\n" + // octal-escaped UTF-8 "café.go"
		"?? \"with space.go\"\n" + // quoted only for the space, no escapes
		"R  \"old name.go\" -> \"new name.go\"\n" + // quoted rename: take the new path
		" M plain/unquoted.go\n" // plain path is untouched
	got := hook.ParseGitStatusPorcelain(out)
	want := []string{"café.go", "with space.go", "new name.go", "plain/unquoted.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGitStatusPorcelain = %#v, want %#v", got, want)
	}
}
