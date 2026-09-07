// Package hook holds the pure, client-side logic the concord-hook binary uses
// to translate Claude Code hook JSON into concord RPCs. It contains no network
// or filesystem calls, so it is unit-testable on its own.
package hook

import (
	"regexp"
	"strings"
)

// ToolInput is the subset of a tool call concord reads.
type ToolInput struct {
	FilePath string `json:"file_path"`
	Command  string `json:"command"`
}

// Input is the subset of Claude Code hook stdin JSON that concord consumes.
type Input struct {
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	ToolName  string    `json:"tool_name"`
	ToolInput ToolInput `json:"tool_input"`
	// Prompt carries a subagent's delegation prompt on SubagentStart. Its exact
	// field name is unverified against the SubagentStart schema (see T09b note).
	Prompt string `json:"prompt"`
}

// ActorID resolves the identity concord keys on: a subagent's agent_id when
// present, otherwise the top-level session_id.
func (i Input) ActorID() string {
	if i.AgentID != "" {
		return i.AgentID
	}
	return i.SessionID
}

var editTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// IsEditTool reports whether name is an edit tool subject to the version check.
func IsEditTool(name string) bool { return editTools[name] }

var shellTools = map[string]bool{
	"Bash":       true,
	"PowerShell": true,
}

// IsShellTool reports whether name is a shell tool whose writes are reconciled
// via the git-status sweep.
func IsShellTool(name string) bool { return shellTools[name] }

// pathLikeToken matches whitespace-free tokens that contain a slash — the
// conservative shape of a repo-relative path in free-text prompts.
var pathLikeToken = regexp.MustCompile(`[A-Za-z0-9_.\-/]*/[A-Za-z0-9_.\-/]+`)

// ExtractPredictedPaths pulls path-like tokens out of a delegation prompt for
// the predicted footprint. It is deliberately conservative (a token must
// contain a slash) since the predicted footprint is advisory; missing a path
// only weakens dedup, never correctness. Results are de-duplicated in order.
func ExtractPredictedPaths(prompt string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range pathLikeToken.FindAllString(prompt, -1) {
		m = strings.Trim(m, "/.,")
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// ParseGitStatusPorcelain extracts the changed file paths from the output of
// `git status --porcelain`. Each line is "XY <path>", with renames written as
// "XY <old> -> <new>"; for a rename the new path is taken.
func ParseGitStatusPorcelain(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:]) // drop the two status columns and the space
		if idx := strings.Index(p, " -> "); idx >= 0 {
			p = p[idx+len(" -> "):]
		}
		p = strings.Trim(p, `"`)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
