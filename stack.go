package commitstack

import (
	"fmt"
	"path/filepath"
	"regexp"
)

// Stack identifies an original commit and its adjacent feedback commits.
// Key is the full SHA for a single commit or <first>-<last> for multiple commits.
// ChangeID is optional lineage metadata from the Change-Id trailer.
// PatchHash and MessageHash identify the reviewable content: the patch and
// the message that a review would show. An attached fixup! commit carries
// no message, so a fixup leaves the message fingerprint unchanged.
type Stack struct {
	Repo        string   `json:"repo"`
	Commits     []string `json:"commits"`
	Base        string   `json:"base"`
	Tip         string   `json:"tip"`
	Subject     string   `json:"subject"`
	Key         string   `json:"key"`
	ChangeID    string   `json:"changeId,omitempty"`
	PatchHash   string   `json:"patchHash,omitempty"`
	MessageHash string   `json:"messageHash,omitempty"`
}

// Validate checks the stack is well-formed.
func (s Stack) Validate() error {
	return s.validate()
}

func (s Stack) validate() error {
	if !filepath.IsAbs(s.Repo) {
		return fmt.Errorf("stack requires an absolute repository path")
	}
	if err := validateCommits(s.Commits); err != nil {
		return err
	}
	if !fullSHA.MatchString(s.Base) {
		return fmt.Errorf("stack requires a full parent SHA")
	}
	if s.Tip != s.Commits[len(s.Commits)-1] {
		return fmt.Errorf("stack tip does not match commits")
	}
	expected := s.Commits[0]
	if len(s.Commits) > 1 {
		expected += stackKeySeparator + s.Tip
	}
	if s.Key != expected {
		return fmt.Errorf("stack key does not match commits")
	}
	if s.Subject == "" {
		return fmt.Errorf("stack requires a subject")
	}
	return s.validateMetadata()
}

func (s Stack) validateMetadata() error {
	if s.PatchHash != "" && !patchHashPattern.MatchString(s.PatchHash) {
		return fmt.Errorf("invalid stack patch hash")
	}
	if s.MessageHash != "" && !messageHashPattern.MatchString(s.MessageHash) {
		return fmt.Errorf("invalid stack message hash")
	}
	if s.ChangeID != "" && !changeIDPattern.MatchString(s.ChangeID) {
		return fmt.Errorf("invalid stack Change-Id")
	}
	return nil
}

// ValidateCommits checks that commits are valid full SHAs without duplicates.
func ValidateCommits(commits []string) error {
	return validateCommits(commits)
}

func validateCommits(commits []string) error {
	if len(commits) == 0 {
		return fmt.Errorf("stack requires commits")
	}
	seen := map[string]bool{}
	for _, sha := range commits {
		if !fullSHA.MatchString(sha) {
			return fmt.Errorf("stack requires full commit SHAs")
		}
		if seen[sha] {
			return fmt.Errorf("duplicate stack commit")
		}
		seen[sha] = true
	}
	return nil
}

// Snapshot is an immutable view of a feature branch's stack range.
type Snapshot struct {
	Branch string  `json:"branch"`
	Target string  `json:"target"`
	Base   string  `json:"base"`
	Head   string  `json:"head"`
	Stacks []Stack `json:"stacks"`
	Repo   string  `json:"repo"`
	Path   string  `json:"statePath,omitempty"`
}

const stackKeySeparator = "-"

var (
	fullSHA            = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	patchHashPattern   = regexp.MustCompile(`^(?:v1:[a-f0-9]{64}|v2:(?:[a-f0-9]{40}|[a-f0-9]{64}))$`)
	messageHashPattern = regexp.MustCompile(`^v1:[a-f0-9]{64}$`)
	feedbackSubject    = regexp.MustCompile(`^(fixup!|reword!|amend!) (.+)$`)
	changeIDPattern    = regexp.MustCompile(`^I[0-9a-fA-F]{40}$`)
)

// SameStack reports whether a and b are the exact same stack.
func SameStack(a, b Stack) bool {
	if a.Repo != b.Repo {
		return false
	}
	if a.Base != b.Base {
		return false
	}
	if a.Key != b.Key {
		return false
	}
	return equalStrings(a.Commits, b.Commits)
}

// SamePatch reports whether a and b share the same patch hash.
func SamePatch(a, b Stack) bool {
	if a.Repo != b.Repo {
		return false
	}
	if a.PatchHash == "" {
		return false
	}
	return a.PatchHash == b.PatchHash
}

// SameMessage reports whether a and b share the same message hash.
func SameMessage(a, b Stack) bool {
	if a.MessageHash == "" {
		return false
	}
	return a.MessageHash == b.MessageHash
}

// SharesLineage reports whether a and b share lineage (same first commit or Change-Id).
func SharesLineage(a, b Stack) bool {
	if a.Repo != b.Repo {
		return false
	}
	if a.Commits[0] == b.Commits[0] {
		return true
	}
	if a.ChangeID == "" {
		return false
	}
	return a.ChangeID == b.ChangeID
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
