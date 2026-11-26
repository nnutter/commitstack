package commitstack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackSelection(t *testing.T) {
	t.Parallel()
	r := testRepo(t)
	commit := func(subject string) string {
		gitTest(t, r.Dir, "commit", "--allow-empty", "-m", subject)
		return gitTest(t, r.Dir, "rev-parse", "HEAD")
	}
	first := commit("First")
	original := commit("Second")
	feedback := commit("fixup! Second")
	tip := commit("fixup! fixup! Second")
	last := commit("Third")
	view, err := r.Snapshot(t.Context())
	require.NoError(t, err)

	middle := []string{original, feedback, tip}
	t.Run("SelectStack", func(t *testing.T) {
		cases := []struct {
			name, selector, wantError string
		}{
			{name: "interior commit", selector: "HEAD~2"},
			{name: "complete stack by refs", selector: "HEAD~3..HEAD~1"},
			{name: "partial stack by refs", selector: "HEAD~3..HEAD~2", wantError: "complete current stack"},
			{name: "outside feature", selector: "main", wantError: "outside the current stack range"},
			{name: "missing ref", selector: "missing-ref", wantError: "rev-parse"},
			{name: "missing start of pair", selector: "missing-ref..HEAD", wantError: "rev-parse"},
			{name: "missing end of pair", selector: "HEAD..missing-ref", wantError: "rev-parse"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := r.SelectStack(t.Context(), view, tc.selector)
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
					return
				}
				require.NoError(t, err)
				require.Equal(t, middle, got.Commits)
			})
		}
	})

	t.Run("SelectRange", func(t *testing.T) {
		cases := []struct {
			name, start, end string
			want             [][]string
			wantError        string
		}{
			{name: "inclusive complete stacks", start: "HEAD~4", end: "HEAD~1", want: [][]string{{first}, middle}},
			{name: "single commit without end", start: "HEAD", want: [][]string{{last}}},
			{name: "starts at feedback", start: feedback, end: "HEAD", wantError: "original commit"},
			{name: "omits trailing feedback", start: first, end: feedback, wantError: "complete stacks"},
			{name: "reversed endpoints", start: "HEAD", end: first, wantError: "oldest-first"},
			{name: "missing start", start: "missing-ref", end: "HEAD", wantError: "rev-parse"},
			{name: "missing end", start: first, end: "missing-ref", wantError: "rev-parse"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := r.SelectRange(t.Context(), view, tc.start, tc.end)
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
					return
				}
				require.NoError(t, err)
				requireStackCommits(t, tc.want, got)
			})
		}
	})

	t.Run("Bounds", func(t *testing.T) {
		cases := []struct {
			name, start, end string
			want             [][]string
			wantError        string
		}{
			{name: "includes stack containing interior end", end: feedback, want: [][]string{{first}, middle}},
			{name: "includes stack containing interior start", start: feedback, want: [][]string{middle, {last}}},
			{name: "both ends unbounded", want: [][]string{{first}, middle, {last}}},
			{name: "outside feature", start: "main", wantError: "outside the current stack range"},
			{name: "reversed endpoints", start: "HEAD", end: first, wantError: "oldest-first"},
			{name: "missing ref", end: "missing-ref", wantError: "rev-parse"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := r.Bounds(t.Context(), view, tc.start, tc.end)
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
					return
				}
				require.NoError(t, err)
				requireStackCommits(t, tc.want, got)
			})
		}
	})
}

func requireStackCommits(t *testing.T, want [][]string, got []Stack) {
	t.Helper()
	require.Len(t, got, len(want))
	for i, commits := range want {
		require.Equal(t, commits, got[i].Commits, "stack %d", i)
	}
}
