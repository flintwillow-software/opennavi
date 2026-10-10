package main

import "testing"

// TestParseGitRemote pins the shapes a repository can arrive in. navi-code
// derives its default egress repository from the working directory's origin
// remote, so a wrong parse would point the agent at the wrong repository.
func TestParseGitRemote(t *testing.T) {
	cases := []struct {
		remote string
		want   string
	}{
		{"git@github.com:acme/widgets.git", "acme/widgets"},
		{"git@github.com:acme/widgets", "acme/widgets"},
		{"https://github.com/acme/widgets.git", "acme/widgets"},
		{"https://github.com/acme/widgets", "acme/widgets"},
		{"https://github.com/acme/widgets/", "acme/widgets"},
		{"ssh://git@github.com/acme/widgets.git", "acme/widgets"},
		// Not a usable owner/name: the caller falls back to "no default",
		// which is not an error (the model may pass repository per call).
		{"", ""},
		{"https://github.com/acme", ""},
		{"https://github.com/acme/widgets/tree/main", ""},
		{"git@github.com:widgets.git", ""},
	}
	for _, tc := range cases {
		if got := parseGitRemote(tc.remote); got != tc.want {
			t.Errorf("parseGitRemote(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}
