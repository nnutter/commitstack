package commitstack_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nnutter/commitstack"
)

func TestStackValidation(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	base, first, tip := objectID("a"), objectID("b"), objectID("c")

	t.Run("single commit without metadata", func(t *testing.T) {
		s := commitstack.Stack{Repo: repo, Base: base, Tip: first, Commits: []string{first}, Key: first, Subject: "Feature"}
		require.NoError(t, s.Validate())
	})

	valid := commitstack.Stack{
		Repo: repo, Base: base, Tip: tip, Commits: []string{first, tip}, Key: first + ".." + tip, Subject: "Feature",
		ChangeID: "I" + objectID("d"), PatchHash: "v2:" + objectID("e"), MessageHash: "v1:" + strings.Repeat("f", 64),
	}
	require.NoError(t, valid.Validate())
	cases := []struct {
		name string
		edit func(*commitstack.Stack)
		want string
	}{
		{"relative repository", func(s *commitstack.Stack) { s.Repo = "repo" }, "absolute repository path"},
		{"no commits", func(s *commitstack.Stack) { s.Commits = nil }, "requires commits"},
		{"abbreviated parent", func(s *commitstack.Stack) { s.Base = "abcdef0" }, "full parent SHA"},
		{"tip precedes feedback", func(s *commitstack.Stack) { s.Tip = first }, "tip"},
		{"key omits feedback", func(s *commitstack.Stack) { s.Key = first }, "key"},
		{"empty subject", func(s *commitstack.Stack) { s.Subject = "" }, "subject"},
		{"malformed patch hash", func(s *commitstack.Stack) { s.PatchHash = "v2:abcd" }, "patch hash"},
		{"malformed message hash", func(s *commitstack.Stack) { s.MessageHash = "v1:abcd" }, "message hash"},
		{"malformed change id", func(s *commitstack.Stack) { s.ChangeID = "Iabcd" }, "Change-Id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.edit(&s)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
}

func TestValidateCommits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		commits []string
		want    string
	}{
		{name: "SHA-256 history", commits: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}},
		{name: "abbreviated commit", commits: []string{"abcdef0"}, want: "full commit SHAs"},
		{name: "nonadjacent duplicate", commits: []string{objectID("a"), objectID("b"), objectID("a")}, want: "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := commitstack.ValidateCommits(tc.commits)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestStackIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	original := commitstack.Stack{
		Repo: filepath.Join(root, "repo"), Base: objectID("a"), Tip: objectID("b"),
		Commits: []string{objectID("b")}, Key: objectID("b"), Subject: "Feature",
		ChangeID: "I" + objectID("c"), PatchHash: "v2:" + objectID("d"), MessageHash: "v1:" + strings.Repeat("e", 64),
	}
	withoutChangeID := original
	withoutChangeID.ChangeID = ""
	withFeedback := withoutChangeID
	withFeedback.Tip = objectID("f")
	withFeedback.Key += ".." + withFeedback.Tip
	withFeedback.Commits = []string{original.Tip, withFeedback.Tip}
	withFeedback.PatchHash = "v2:" + objectID("1")

	rebased := original
	rebased.Base = objectID("2")
	rebased.Tip = objectID("3")
	rebased.Key = rebased.Tip
	rebased.Commits = []string{rebased.Tip}

	independent := rebased
	independent.ChangeID = ""
	independent.Subject = "Independent feature"
	independent.MessageHash = "v1:" + strings.Repeat("4", 64)

	otherRepo := original
	otherRepo.Repo = filepath.Join(root, "other")

	withoutFingerprints := original
	withoutFingerprints.PatchHash = ""
	withoutFingerprints.MessageHash = ""

	cases := []struct {
		name                           string
		a, b                           commitstack.Stack
		stack, patch, message, lineage bool
	}{
		{name: "same commits without fingerprints", a: withoutFingerprints, b: withoutFingerprints, stack: true, lineage: true},
		{name: "feedback changes patch only", a: withoutChangeID, b: withFeedback, message: true, lineage: true},
		{name: "rebase preserves content and change id", a: original, b: rebased, patch: true, message: true, lineage: true},
		{name: "matching patches without shared lineage", a: withoutChangeID, b: independent, patch: true},
		{name: "identical commits in another repository", a: original, b: otherRepo, message: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.stack, commitstack.SameStack(tc.a, tc.b), "same stack")
			assert.Equal(t, tc.patch, commitstack.SamePatch(tc.a, tc.b), "same patch")
			assert.Equal(t, tc.message, commitstack.SameMessage(tc.a, tc.b), "same message")
			assert.Equal(t, tc.lineage, commitstack.SharesLineage(tc.a, tc.b), "shares lineage")
		})
	}
}

func objectID(digit string) string {
	return strings.Repeat(digit, 40)
}
