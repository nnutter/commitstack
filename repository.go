package commitstack

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/log/v2"
)

// Repository executes Git in the source checkout.
type Repository struct {
	Dir              string
	Environment      []string
	Logger           *log.Logger
	missingIDWarning *sync.Once
}

// Bounds returns stacks whose commits intersect [first, last] inclusive.
// Empty first/last means unbounded on that side.
func (r Repository) Bounds(ctx context.Context, view Snapshot, first, last string) ([]Stack, error) {
	start, end := 0, len(view.Stacks)-1
	for _, endpoint := range []struct {
		ref   string
		index *int
	}{{first, &start}, {last, &end}} {
		if endpoint.ref == "" {
			continue
		}
		sha, err := r.Resolve(ctx, endpoint.ref)
		if err != nil {
			return nil, err
		}
		found := false
		for i, stack := range view.Stacks {
			if slices.Contains(stack.Commits, sha) {
				*endpoint.index = i
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("boundary %s is outside the current stack range", endpoint.ref)
		}
	}
	if len(view.Stacks) == 0 {
		return view.Stacks, nil
	}
	if end < start {
		return nil, fmt.Errorf("boundaries must be in oldest-first order")
	}
	return view.Stacks[start : end+1], nil
}

// BranchView identifies the current branch and its upstream without loading
// the stack range or computing patch fingerprints.
func (r Repository) BranchView(ctx context.Context) (Snapshot, string, error) {
	view, repo, err := r.checkoutView(ctx)
	if err != nil {
		return view, "", err
	}
	target, err := r.git(ctx, "rev-parse", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return view, "", fmt.Errorf("branch %s requires a configured Git upstream: %w", view.Branch, err)
	}
	view.Target = target
	return view, repo, nil
}

// CheckoutView returns branch and repo info without requiring upstream or a full stack range.
func (r Repository) CheckoutView(ctx context.Context) (Snapshot, string, error) {
	return r.checkoutView(ctx)
}

// Environment exposes the repository environment.
func (r Repository) EnvironmentValue(key string) string {
	return environmentValue(r.Environment, key)
}

// Git delegates to git for callers that need raw git access.
func (r Repository) Git(ctx context.Context, args ...string) (string, error) {
	return r.git(ctx, args...)
}

// GitInput delegates to gitInput.
func (r Repository) GitInput(ctx context.Context, input string, args ...string) (string, error) {
	return r.gitInput(ctx, input, args...)
}

// GitOutput delegates to gitOutput.
func (r Repository) GitOutput(ctx context.Context, args ...string) (string, error) {
	return r.gitOutput(ctx, args...)
}

// PatchHash exposes the patch fingerprint for testing and review.
func (r Repository) PatchHash(ctx context.Context, base, tip string) (string, error) {
	return r.patchHash(ctx, base, tip)
}

// PruneCache applies the maintenance retention limits to the shared cache.
// Repository discovery does not require a branch, upstream, or stack history.
func (r Repository) PruneCache(ctx context.Context) error {
	common, err := r.git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	return prunePatchCache(ctx, filepath.Join(common, "timber-review", "cache", "patch-v2"))
}

// Resolve accepts a commit reference but never interprets it as an option.
func (r Repository) Resolve(ctx context.Context, ref string) (string, error) {
	return r.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

// SelectRange returns the inclusive range of stacks between first and last endpoints.
// First must be an original commit, last must be a tip or commit inside a stack.
// Complete stacks are returned in oldest-first order.
func (r Repository) SelectRange(ctx context.Context, view Snapshot, first, last string) ([]Stack, error) {
	startSHA, err := r.Resolve(ctx, first)
	if err != nil {
		return nil, err
	}
	endSHA := startSHA
	if last != "" {
		endSHA, err = r.Resolve(ctx, last)
		if err != nil {
			return nil, err
		}
	}
	start, end := -1, -1
	for i, stack := range view.Stacks {
		if stack.Commits[0] == startSHA {
			start = i
		}
		if stack.Tip == endSHA {
			end = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("range must start at an original commit in the stack range")
	}
	if end < start {
		return nil, fmt.Errorf("range requires complete stacks in oldest-first order; include all attached fixups")
	}
	return view.Stacks[start : end+1], nil
}

// SelectStack returns the stack that contains selector or matches selector's key.
// Selector can be a commit reference inside a stack or a "<from>..<to>" range.
func (r Repository) SelectStack(ctx context.Context, view Snapshot, selector string) (Stack, error) {
	if first, last, ok := strings.Cut(selector, ".."); ok {
		start, err := r.Resolve(ctx, first)
		if err != nil {
			return Stack{}, err
		}
		end, err := r.Resolve(ctx, last)
		if err != nil {
			return Stack{}, err
		}
		for _, stack := range view.Stacks {
			if stack.Key == start+".."+end {
				return stack, nil
			}
		}
		return Stack{}, fmt.Errorf("stack endpoints must select one complete current stack")
	}
	sha, err := r.Resolve(ctx, selector)
	if err != nil {
		return Stack{}, err
	}
	for _, stack := range view.Stacks {
		if slices.Contains(stack.Commits, sha) {
			return stack, nil
		}
	}
	return Stack{}, fmt.Errorf("commit is outside the current stack range")
}

// Snapshot requires an upstream and a single, linear feature-branch range.
func (r Repository) Snapshot(ctx context.Context) (Snapshot, error) {
	started := time.Now()
	defer func() { r.logger().Debug("Repository snapshot completed", "elapsed", time.Since(started)) }()
	view, repo, err := r.stackView(ctx)
	if err != nil {
		return view, err
	}
	cache := newPatchCache(ctx, r, view, repo)
	for i := range view.Stacks {
		stack := &view.Stacks[i]
		stack.PatchHash, err = cache.hash(ctx, r, stack.Base, stack.Tip)
		if err != nil {
			return view, err
		}
	}
	return view, nil
}

// WarmCache fingerprints only the current feature stack containing ref.
// An empty ref selects HEAD. Missing entries for other stacks remain untouched.
func (r Repository) WarmCache(ctx context.Context, ref string) error {
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := r.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	view, repo, err := r.stackView(ctx)
	if err != nil {
		return err
	}
	for _, stack := range view.Stacks {
		if !slices.Contains(stack.Commits, sha) {
			continue
		}
		view.Stacks = []Stack{stack}
		cache := newPatchCache(ctx, r, view, repo)
		_, err := cache.hash(ctx, r, stack.Base, stack.Tip)
		return err
	}
	return fmt.Errorf("commit %s is not in the current feature range", sha)
}

func (r Repository) checkoutView(ctx context.Context) (Snapshot, string, error) {
	var view Snapshot
	repo, err := r.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return view, "", err
	}
	view.Repo = repo
	branch, err := r.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return view, "", fmt.Errorf("review requires a branch, not detached HEAD: %w", err)
	}
	common, err := r.git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return view, "", err
	}
	view.Branch = branch
	view.Path = filepath.Join(common, "timber-review", "state", fmt.Sprintf("%x.json", sha256.Sum256([]byte(branch))))
	return view, repo, nil
}

func (r Repository) environment() []string {
	if r.Environment == nil {
		return os.Environ()
	}
	return r.Environment
}

func (r Repository) git(ctx context.Context, args ...string) (string, error) {
	out, err := r.gitOutput(ctx, args...)
	return strings.TrimSpace(out), err
}

func (r Repository) gitInput(ctx context.Context, input string, args ...string) (string, error) {
	started := time.Now()
	defer func() { r.logger().Debug("Git command completed", "args", args, "elapsed", time.Since(started)) }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(slices.Clone(r.environment()), "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.HasPrefix(stderr.String(), "fatal: not a git repository") {
			return "", fmt.Errorf("timber review must be used from inside a Git repository")
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (r Repository) gitOutput(ctx context.Context, args ...string) (string, error) {
	return r.gitInput(ctx, "", args...)
}

func (r Repository) logger() *log.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return quietLogger
}

// patchHash fingerprints the aggregate diff, not commit metadata. Empty diffs
// have no content identity, so unrelated empty commits cannot share approval.
func (r Repository) patchHash(ctx context.Context, base, tip string) (string, error) {
	patch, err := r.gitOutput(ctx, slices.Concat(DiffOptions, []string{base, tip, "--"})...)
	if err != nil {
		return "", err
	}
	if patch == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "patch-id", "--verbatim")
	cmd.Dir = r.Dir
	cmd.Env = append(slices.Clone(r.environment()), "LC_ALL=C")
	cmd.Stdin = strings.NewReader(patch)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git patch-id --verbatim: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || !fullSHA.MatchString(fields[0]) {
		return "", fmt.Errorf("git patch-id --verbatim returned an invalid patch ID")
	}
	return "v2:" + fields[0], nil
}

func (r Repository) stackView(ctx context.Context) (Snapshot, string, error) {
	view, repo, err := r.BranchView(ctx)
	if err != nil {
		return view, repo, err
	}
	view.Head, err = r.Resolve(ctx, "HEAD")
	if err != nil {
		return view, repo, err
	}
	targetSHA, err := r.Resolve(ctx, view.Target)
	if err != nil {
		return view, repo, err
	}
	view.Base, err = r.git(ctx, "merge-base", "--all", view.Head, targetSHA)
	if err != nil {
		return view, repo, err
	}
	if strings.Contains(view.Base, "\n") {
		return view, repo, fmt.Errorf("review requires one merge-base with upstream")
	}
	history, err := readHistory(ctx, r, repo, view.Base, view.Head)
	if err != nil {
		return view, repo, err
	}
	view.Stacks, err = stacks(repo, view.Base, history)
	if err != nil {
		return view, repo, err
	}
	return view, repo, nil
}

var quietLogger = log.New(io.Discard)

// New creates a Repository for dir with an optional environment and logger.
func New(dir string, env []string) Repository {
	return Repository{Dir: dir, Environment: env}
}

// NewWithLogger creates a Repository with an explicit logger.
func NewWithLogger(dir string, env []string, logger *log.Logger) Repository {
	return Repository{Dir: dir, Environment: env, Logger: logger, missingIDWarning: new(sync.Once)}
}

func environmentValue(environment []string, key string) string {
	if environment == nil {
		return os.Getenv(key)
	}
	value := ""
	prefix := key + "="
	for _, entry := range environment {
		if found, ok := strings.CutPrefix(entry, prefix); ok {
			value = found
		}
	}
	return value
}

// PathHash is exposed for review state path construction.
func PathHash(branch string) [32]byte {
	return sha256.Sum256([]byte(branch))
}

// DiffOptions pins diff inputs that change rendered patch bytes.
var DiffOptions = []string{
	"-c", "core.quotePath=true", "-c", "diff.suppressBlankEmpty=false", "diff",
	"--patch", "--binary", "--full-index", "--no-renames", "--no-ext-diff",
	"--no-textconv", "--no-color", "--no-relative", "--ignore-submodules=none",
	"--src-prefix=a/", "--dst-prefix=b/", "--output-indicator-new=+",
	"--output-indicator-old=-", "--output-indicator-context= ",
	"--unified=3", "--inter-hunk-context=0", "--diff-algorithm=myers",
	"--no-indent-heuristic", "--submodule=short", "-O/dev/null",
}

func stacks(repo, base string, history []historyCommit) ([]Stack, error) {
	stacks := []Stack{}
	if len(history) == 0 {
		return stacks, nil
	}
	parent := base
	ids := map[string]bool{}
	targets := map[string][]int{}
	for _, commit := range history {
		if commit.parent != parent {
			return nil, fmt.Errorf("review requires linear feature history; resolve merges first")
		}
		sha, subject := commit.sha, commit.subject
		if subject == "" {
			return nil, fmt.Errorf("commit %s has an empty subject", sha)
		}
		if match := feedbackSubject.FindStringSubmatch(subject); match != nil {
			if !tryAppendFeedback(stacks, targets, sha, match[2]) {
				id, err := changeID(sha, commit.changeID, ids)
				if err != nil {
					return nil, err
				}
				stacks = append(stacks, Stack{Repo: repo, Commits: []string{sha}, Base: parent, Tip: sha, Subject: subject, Key: sha, ChangeID: id})
			}
		} else {
			id, err := changeID(sha, commit.changeID, ids)
			if err != nil {
				return nil, err
			}
			stacks = append(stacks, Stack{Repo: repo, Commits: []string{sha}, Base: parent, Tip: sha, Subject: subject, Key: sha, ChangeID: id})
		}
		targets[subject] = append(targets[subject], len(stacks)-1)
		parent = sha
	}
	if err := fingerprintMessages(stacks, history); err != nil {
		return nil, err
	}
	return stacks, nil
}

func fingerprintMessages(stacks []Stack, history []historyCommit) error {
	next := 0
	for i := range stacks {
		end := next + len(stacks[i].Commits)
		if end > len(history) {
			return fmt.Errorf("stack does not match feature history")
		}
		stacks[i].MessageHash = messageFingerprint(history[next:end])
		next = end
	}
	if next != len(history) {
		return fmt.Errorf("stack does not match feature history")
	}
	return nil
}

func changeID(sha, id string, ids map[string]bool) (string, error) {
	if id == "" {
		return "", nil
	}
	if !changeIDPattern.MatchString(id) {
		return "", fmt.Errorf("commit %s requires at most one valid Change-Id trailer", sha)
	}
	if ids[id] {
		return "", fmt.Errorf("duplicate Change-Id %s on current original commits", id)
	}
	ids[id] = true
	return id, nil
}

func tryAppendFeedback(stacks []Stack, targets map[string][]int, sha, subject string) bool {
	matches := targets[subject]
	if len(matches) != 1 {
		return false
	}
	if matches[0] != len(stacks)-1 {
		return false
	}
	last := &stacks[len(stacks)-1]
	last.Commits = append(last.Commits, sha)
	last.Tip = sha
	last.Key = last.Commits[0] + ".." + sha
	return true
}
