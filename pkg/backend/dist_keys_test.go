package backend

import (
	"errors"
	"path"
	"testing"
)

const firstPrefix = "first-"

// TestBuildKeyMatcher_Modes covers the three classifier branches —
// empty (match-all), prefix (no metacharacters), and glob (any of
// `*?[`). Anchors the prefix-vs-glob switch so adding a new metachar
// here later requires an intentional test update.
func TestBuildKeyMatcher_Modes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		key     string
		want    bool
	}{
		// Empty: every key matches.
		{"empty matches anything", "", "anything", true},
		{"empty matches empty key", "", "", true},

		// Prefix: HasPrefix semantics.
		{"prefix hit", firstPrefix, "first-1", true},
		{"prefix miss", firstPrefix, "second-1", false},
		{"prefix exact", firstPrefix, firstPrefix, true},
		{"prefix too short", firstPrefix, "first", false},

		// Glob via path.Match.
		{"glob star matches sequence", "first-*", "first-123", true},
		{"glob star matches empty tail", "first-*", firstPrefix, true},
		{"glob star miss different prefix", "first-*", "second-123", false},
		{"glob question matches single", "first-?", "first-1", true},
		{"glob question miss two chars", "first-?", "first-12", false},
		{"glob class match", "[abc]*", "alpha", true},
		{"glob class miss", "[abc]*", "delta", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matcher, err := buildKeyMatcher(tt.pattern)
			if err != nil {
				t.Fatalf("buildKeyMatcher(%q): unexpected error %v", tt.pattern, err)
			}

			got := matcher(tt.key)
			if got != tt.want {
				t.Fatalf("buildKeyMatcher(%q)(%q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
			}
		})
	}
}

// TestBuildKeyMatcher_InvalidGlob pins that a malformed glob (e.g.
// unclosed `[`) surfaces an error at construction rather than every
// match failing silently at runtime.
func TestBuildKeyMatcher_InvalidGlob(t *testing.T) {
	t.Parallel()

	_, err := buildKeyMatcher("[unclosed")
	if err == nil {
		t.Fatalf("expected error for unclosed character class, got nil")
	}

	if !errors.Is(err, path.ErrBadPattern) {
		t.Fatalf("expected wrapped path.ErrBadPattern, got %v", err)
	}
}
