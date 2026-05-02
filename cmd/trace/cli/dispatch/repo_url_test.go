package dispatch

import "testing"

func TestGitHubRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fullName string
		want     string
	}{
		{
			name:     "valid",
			fullName: "GrayCodeAI/cli",
			want:     testRepoURL,
		},
		{
			name:     "valid punctuation in repo",
			fullName: "GrayCodeAI/trace.io",
			want:     "https://github.com/GrayCodeAI/trace.io",
		},
		{
			name:     "missing slash",
			fullName: "GrayCodeAI",
			want:     "",
		},
		{
			name:     "nested path",
			fullName: "GrayCodeAI/cli/issues",
			want:     "",
		},
		{
			name:     "unsafe owner",
			fullName: "-GrayCodeAI/cli",
			want:     "",
		},
		{
			name:     "unsafe repo",
			fullName: "GrayCodeAI/cli)",
			want:     "",
		},
		{
			name:     "dot repo",
			fullName: "GrayCodeAI/.",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := githubRepoURL(tt.fullName); got != tt.want {
				t.Fatalf("githubRepoURL(%q) = %q, want %q", tt.fullName, got, tt.want)
			}
		})
	}
}
