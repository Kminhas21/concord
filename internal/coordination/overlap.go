package coordination

import (
	"path"
	"strings"
)

// pathTokens expands a path to the tokens used for literal overlap: its
// normalized full path and, when it has one, its parent directory. Two paths
// overlap when their token sets intersect — the same file or the same
// directory. A bare top-level file contributes only its full path, so unrelated
// root-level files do not collide on the "." directory.
func pathTokens(p string) []string {
	n := strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if n == "" {
		return nil
	}
	tokens := []string{n}
	if dir := path.Dir(n); dir != "." && dir != "/" && dir != "" {
		tokens = append(tokens, dir)
	}
	return tokens
}

// divergentPaths returns the actual paths that fall outside the predicted
// footprint — those sharing no token with any predicted path. When no scope was
// predicted, every actual path is divergent.
func divergentPaths(predicted, actual []string) []string {
	var out []string
	for _, a := range actual {
		if !pathsOverlap([]string{a}, predicted) {
			out = append(out, a)
		}
	}
	return out
}

// pathsOverlap reports whether any path in a shares a token with any path in b.
func pathsOverlap(a, b []string) bool {
	set := make(map[string]struct{})
	for _, p := range a {
		for _, tok := range pathTokens(p) {
			set[tok] = struct{}{}
		}
	}
	for _, p := range b {
		for _, tok := range pathTokens(p) {
			if _, ok := set[tok]; ok {
				return true
			}
		}
	}
	return false
}
