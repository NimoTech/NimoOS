package v1

import (
	"testing"
	"time"
)

func TestStaleClientIDs(t *testing.T) {
	now := time.Now()
	const timeout = 90 * time.Second

	cases := []struct {
		name      string
		lastBeats map[string]time.Time
		want      []string
	}{
		{
			name:      "empty map",
			lastBeats: map[string]time.Time{},
			want:      nil,
		},
		{
			name: "no client timed out",
			lastBeats: map[string]time.Time{
				"a": now.Add(-10 * time.Second),
				"b": now,
			},
			want: nil,
		},
		{
			name: "one client timed out",
			lastBeats: map[string]time.Time{
				"a": now.Add(-100 * time.Second),
				"b": now,
			},
			want: []string{"a"},
		},
		{
			name: "exactly at boundary is not stale",
			lastBeats: map[string]time.Time{
				"a": now.Add(-timeout),
			},
			want: nil,
		},
		{
			name: "just past boundary is stale",
			lastBeats: map[string]time.Time{
				"a": now.Add(-timeout - time.Millisecond),
			},
			want: []string{"a"},
		},
		{
			name: "multiple clients timed out",
			lastBeats: map[string]time.Time{
				"a": now.Add(-200 * time.Second),
				"b": now,
				"c": now.Add(-91 * time.Second),
			},
			want: []string{"a", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleClientIDs(tc.lastBeats, now, timeout)
			assertSameIDs(t, got, tc.want)
		})
	}
}

// assertSameIDs compares two ID slices as sets, since staleClientIDs iterates
// a map and cannot guarantee output order.
func assertSameIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("staleClientIDs() = %v, want %v", got, want)
	}
	seen := make(map[string]bool, len(want))
	for _, id := range want {
		seen[id] = true
	}
	for _, id := range got {
		if !seen[id] {
			t.Fatalf("staleClientIDs() = %v, want %v (unexpected id %q)", got, want, id)
		}
		delete(seen, id)
	}
	if len(seen) != 0 {
		t.Fatalf("staleClientIDs() = %v, want %v (missing ids)", got, want)
	}
}
