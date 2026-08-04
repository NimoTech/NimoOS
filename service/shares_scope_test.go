package service

import "testing"

// sharePathScope determines the SQL matching range for "which shares to
// clean up when deleting a directory" — the boundary must be exact:
// deleting /a/b must cover /a/b itself and /a/b/c, and must never touch
// /a/bc.
func TestSharePathScope(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		exact   string
		subtree string
	}{
		{"ordinary path", "/a/b", "/a/b", "/a/b/%"},
		{"trailing slash normalized", "/a/b/", "/a/b", "/a/b/%"},
		{"LIKE wildcard escaping", "/a/100%_done", "/a/100%_done", `/a/100\%\_done/%`},
		{"root path", "/", "/", "/%"},
		{"empty string treated as root", "", "/", "/%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exact, subtree := sharePathScope(c.in)
			if exact != c.exact || subtree != c.subtree {
				t.Fatalf("sharePathScope(%q) = (%q, %q), want (%q, %q)", c.in, exact, subtree, c.exact, c.subtree)
			}
		})
	}
}

// TestRewriteSharePath locks down the pure-function semantics of "rewriting
// share paths when moving/renaming a directory that contains shares":
// exactly oldExact itself is replaced wholesale with newExact; inside its
// subtree, a prefix replacement is done; outside the range (including
// same-prefix sibling directories and ancestor paths) it's returned
// unchanged.
func TestRewriteSharePath(t *testing.T) {
	cases := []struct {
		name      string
		sharePath string
		oldExact  string
		newExact  string
		want      string
	}{
		{"exactly itself", "/a/b", "/a/b", "/x", "/x"},
		{"one level inside subtree", "/a/b/c", "/a/b", "/x", "/x/c"},
		{"multiple levels inside subtree", "/a/b/c/d/e", "/a/b", "/x/y", "/x/y/c/d/e"},
		{"out of range: same-prefix sibling", "/a/bc", "/a/b", "/x", "/a/bc"},
		{"out of range: ancestor", "/a", "/a/b", "/x", "/a"},
		{"out of range: completely unrelated", "/z/q", "/a/b", "/x", "/z/q"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rewriteSharePath(c.sharePath, c.oldExact, c.newExact)
			if got != c.want {
				t.Fatalf("rewriteSharePath(%q, %q, %q) = %q, want %q", c.sharePath, c.oldExact, c.newExact, got, c.want)
			}
		})
	}
}

// Self-evident semantics: under the subtree-pattern + "/" boundary
// combination, /a/bc must not match /a/b's cleanup scope. (Simulates LIKE
// semantics via an equivalent Go-side prefix check; the real SQL is
// assembled by DeleteShareByPath, and the pattern string itself is already
// locked down by the cases above.)
func TestSharePathScopeBoundary(t *testing.T) {
	exact, _ := sharePathScope("/a/b")
	for path, want := range map[string]bool{
		"/a/b":    true,  // the share is mounted right on the directory being deleted
		"/a/b/c":  true,  // inside the subtree
		"/a/bc":   false, // same-prefix sibling directory, must never be deleted
		"/a":      false, // ancestor
		"/a/b2/c": false,
	} {
		got := path == exact || (len(path) > len(exact)+1 && path[:len(exact)+1] == exact+"/")
		if got != want {
			t.Fatalf("path %q in scope of /a/b = %v, want %v", path, got, want)
		}
	}
}
