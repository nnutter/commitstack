// Package commitstack tracks commit stacks on a feature branch.
//
// A stack is an original commit plus any attached autosquash feedback
// commits (fixup!, reword!, amend!). Stack detection walks the linear
// feature history between the merge base and HEAD, groups feedback commits
// onto their target, and fingerprints both the patch and the effective
// commit message so rewrites that keep content can be matched across
// revisions.
//
// The package has no dependency on tuicr or any review UI. Callers that
// need review-state tracking (attempts, approvals, saved sessions) live
// outside this package; commit-stack only answers which stacks exist and
// what content they carry.
package commitstack
