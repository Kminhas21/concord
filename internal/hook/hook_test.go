package hook_test

import (
	"encoding/json"
	"reflect"
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

func TestToolClassification(t *testing.T) {
	if !hook.IsEditTool("Write") || hook.IsEditTool("Read") {
		t.Fatal("edit-tool classification wrong")
	}
	if !hook.IsShellTool("Bash") || !hook.IsShellTool("PowerShell") || hook.IsShellTool("Edit") {
		t.Fatal("shell-tool classification wrong")
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
