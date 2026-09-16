// Package hook holds the pure, client-side logic the concord-hook binary uses
// to translate Claude Code hook JSON into concord RPCs. It contains no network
// or filesystem calls, so it is unit-testable on its own.
package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// ToolInput is the subset of a tool call concord reads.
type ToolInput struct {
	FilePath string `json:"file_path"`
	// NotebookPath is the target of the NotebookEdit tool, which does not use
	// file_path.
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
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

// EditPath returns the file an edit tool targets: file_path for most edit tools,
// falling back to notebook_path for NotebookEdit. Without this fallback,
// NotebookEdit edits would carry an empty path and silently skip the version
// check.
func (i Input) EditPath() string {
	if i.ToolInput.FilePath != "" {
		return i.ToolInput.FilePath
	}
	return i.ToolInput.NotebookPath
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

// RecordReads reports whether read footprint should be recorded, from the
// value of CONCORD_RECORD_READS. It is the opt-in for exploration dedup: off by
// default, so refactor/mechanical agents pay only for write footprint.
func RecordReads(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// RepoRelative maps a path to a form relative to the repo root, using
// forward slashes. A path already relative, or outside the root, or given no
// root, is returned normalized but unchanged. This gives every ingestion point
// (edit tools pass absolute paths; git status passes root-relative ones) one
// common path scale, so predicted and actual footprints compare correctly.
func RepoRelative(root, p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if root == "" {
		return p
	}
	root = strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
	switch {
	case p == root:
		return "."
	case strings.HasPrefix(p, root+"/"):
		return p[len(root)+1:]
	default:
		return p
	}
}

// FoldCase lowercases a path key on case-insensitive filesystems (Windows,
// macOS) so that Foo.txt and foo.txt resolve to the same concord key; on
// case-sensitive filesystems the path is returned unchanged.
func FoldCase(p string, caseInsensitive bool) string {
	if caseInsensitive {
		return strings.ToLower(p)
	}
	return p
}

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

// SnapshotName is the base filename of the per-actor shell snapshot: the
// before-command dirty-file hashes the reconcile sweep diffs against. It is
// derived from a hash of the actor id so any actor id yields one stable,
// filesystem-safe name with no separators.
func SnapshotName(actor string) string {
	sum := sha256.Sum256([]byte(actor))
	return "concord-shell-" + hex.EncodeToString(sum[:]) + ".json"
}

// ReconcileTargets attributes a shell command's writes by content delta. Given
// the hashes of the dirty files just before the command (before) and just after
// (after), it returns the paths the command actually wrote: those present in
// after whose hash appeared or changed since before. A file already dirty and
// left untouched by the command — a human's out-of-band edit, say — has an equal
// before/after hash and is excluded, so reconciling never advances the actor's
// read-hash onto a change it did not make (the US3 protection). The result is
// sorted for determinism.
func ReconcileTargets(before, after map[string]string) []string {
	var out []string
	for p, h := range after {
		if before[p] != h {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ParseGitStatusPorcelain extracts the changed file paths from the output of
// `git status --porcelain`. Each line is "XY <path>", with renames written as
// "XY <old> -> <new>"; for a rename the new path is taken. git C-quotes any path
// with unusual bytes (a non-ASCII byte is octal-escaped under the default
// core.quotepath; a space forces quoting regardless), so a quoted field is
// decoded back to the real on-disk name — otherwise the reconcile key would
// never match what the edit tool sent.
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
		p = unquoteGitPath(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// unquoteGitPath decodes a git C-quoted path ("...") back to its literal bytes,
// handling the standard escapes and \NNN octal (which reconstructs octal-escaped
// UTF-8). A path git left unquoted is returned unchanged.
func unquoteGitPath(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	var b []byte
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' {
			b = append(b, c)
			continue
		}
		i++
		if i >= len(inner) {
			break
		}
		switch e := inner[i]; e {
		case 'a':
			b = append(b, '\a')
		case 'b':
			b = append(b, '\b')
		case 't':
			b = append(b, '\t')
		case 'n':
			b = append(b, '\n')
		case 'v':
			b = append(b, '\v')
		case 'f':
			b = append(b, '\f')
		case 'r':
			b = append(b, '\r')
		case '"', '\\':
			b = append(b, e)
		default:
			if e >= '0' && e <= '7' { // \NNN octal, up to three digits
				val := int(e - '0')
				for k := 0; k < 2 && i+1 < len(inner) && inner[i+1] >= '0' && inner[i+1] <= '7'; k++ {
					i++
					val = val*8 + int(inner[i]-'0')
				}
				b = append(b, byte(val))
			} else {
				b = append(b, e) // unknown escape: keep the char verbatim
			}
		}
	}
	return string(b)
}
