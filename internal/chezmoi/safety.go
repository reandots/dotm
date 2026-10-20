package chezmoi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type checkout struct {
	repository  string
	head        string
	fingerprint [sha256.Size]byte
	info        os.FileInfo
}

// Reject configurations that can hide work or depend on another checkout.
// Ordinary Git administration files are preserved in the retained backup.
func inspectCheckout(ctx context.Context, path string) (checkout, error) {
	return inspectCheckoutMode(ctx, path, true)
}

func inspectCheckoutMode(ctx context.Context, path string, requireClean bool) (checkout, error) {
	var state checkout
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.IsDir() {
		return state, fmt.Errorf("source root must be a real directory")
	}
	state.info = info
	gitDir := filepath.Join(path, ".git")
	info, err = os.Lstat(gitDir)
	if err != nil || !info.IsDir() {
		return state, fmt.Errorf("source must be a standalone Git checkout with a real .git directory")
	}
	for _, name := range []string{
		"commondir", "shallow", "objects/info/alternates", "info/grafts", "worktrees", "modules",
	} {
		if _, statErr := os.Lstat(filepath.Join(gitDir, name)); !os.IsNotExist(statErr) {
			return state, fmt.Errorf("incompatible source Git state: .git/%s", name)
		}
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return state, fmt.Errorf("inspect source repository: %w", err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(top) != real {
		return state, fmt.Errorf("source is not the root of its Git working copy")
	}
	if sparse, _ := git(ctx, path, "config", "--bool", "core.sparseCheckout"); sparse == "true" {
		return state, fmt.Errorf("sparse source checkouts are unsupported")
	}
	flags, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return state, err
	}
	for _, entry := range strings.Split(flags, "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return state, fmt.Errorf("source index contains hidden working-copy files (skip-worktree/assume-unchanged)")
		}
	}
	stages, err := git(ctx, path, "ls-files", "--stage", "-z")
	if err != nil {
		return state, err
	}
	for _, entry := range strings.Split(stages, "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return state, fmt.Errorf("source submodules are unsupported")
		}
	}
	if requireClean {
		status, statusErr := git(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all",
			"--ignored=matching", "--ignore-submodules=none", "--no-renames")
		if statusErr != nil {
			return state, statusErr
		}
		if status != "" {
			var message strings.Builder
			fmt.Fprintf(&message, "source repository %q contains changed, untracked, or ignored data; "+
				"refusing to sync it:\n", path)
			for _, entry := range strings.Split(status, "\x00") {
				if len(entry) >= 4 {
					kind := "changed"
					switch entry[:2] {
					case "??":
						kind = "untracked"
					case "!!":
						kind = "ignored"
					}
					fmt.Fprintf(&message, "  %s: %q\n", kind, entry[3:])
				}
			}
			message.WriteString("Review these paths. Commit intended source changes and move unrelated data " +
				"outside the checkout before retrying. No files were discarded.")
			return state, fmt.Errorf("%s", message.String())
		}
	}
	if requireClean {
		files, filesErr := git(ctx, path, "ls-files", "-z")
		if filesErr != nil {
			return state, filesErr
		}
		if directoryErr := checkUntrackedDirectories(path, files); directoryErr != nil {
			return state, directoryErr
		}
	}
	state.repository, err = git(ctx, path, "config", "--get-all", "remote.origin.url")
	if err != nil || state.repository == "" || strings.Contains(state.repository, "\n") {
		return state, fmt.Errorf("source must have exactly one origin URL")
	}
	state.repository, err = git(ctx, path, "remote", "get-url", "--all", "origin")
	if err != nil || state.repository == "" || strings.Contains(state.repository, "\n") {
		return state, fmt.Errorf("source must have exactly one effective origin URL")
	}
	if local, ok := localRepositoryPath(state.repository); ok && !filepath.IsAbs(local) {
		state.repository = filepath.Join(path, local)
	}
	state.head, err = git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return state, fmt.Errorf("source has no valid HEAD commit: %w", err)
	}
	refs, err := git(ctx, path, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return state, err
	}
	if strings.Contains(refs, "refs/replace/") {
		return state, fmt.Errorf("source replacement refs are unsupported")
	}
	config, err := git(ctx, path, "config", "--local", "--null", "--list")
	if err != nil {
		return state, err
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return state, err
	}
	state.fingerprint = sha256.Sum256([]byte(state.head + "\x00" + string(head) + "\x00" + refs + "\x00" + config))
	return state, nil
}

// Git status omits empty directories. They are still user data and must not be
// silently lost on replacement, including empty ignored/nested repositories.
func checkUntrackedDirectories(root, files string) error {
	known := map[string]bool{".": true}
	for _, file := range strings.Split(files, "\x00") {
		if file == "" {
			continue
		}
		known[filepath.FromSlash(file)] = true
		for parent := filepath.Dir(file); parent != "."; parent = filepath.Dir(parent) {
			known[filepath.FromSlash(parent)] = true
		}
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			return filepath.SkipDir
		}
		if !known[relative] {
			return fmt.Errorf("source repository %q has untracked data at %q; refusing synchronization; "+
				"move unrelated data outside the checkout before retrying. Git does not track empty directories; "+
				"no files were discarded", root, relative)
		}
		return nil
	})
}

// A fresh mirror is essential: fetching into the old checkout cannot prove that
// its existing objects are actually present remotely. Check all local objects,
// including reflog-only/dangling work, tags, noncurrent branches and blob refs.
// This intentionally refuses even harmless unreachable objects absent remotely.
func provePublished(ctx context.Context, path, repository string) error {
	temporary, err := os.MkdirTemp(filepath.Dir(path), ".dotm-source-proof-")
	if err != nil {
		return fmt.Errorf("create remote proof directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	mirror := filepath.Join(temporary, "mirror")
	if _, cloneErr := git(ctx, "", "clone", "--mirror", "--no-local", "--template=",
		"--", repository, mirror); cloneErr != nil {
		return fmt.Errorf("cannot prove old source is preserved remotely: %w", cloneErr)
	}
	remoteObjects, err := git(ctx, mirror, "rev-list", "--objects", "--all", "--no-object-names")
	if err != nil {
		return err
	}
	published := make(map[string]struct{})
	for _, object := range strings.Fields(remoteObjects) {
		published[object] = struct{}{}
	}
	localObjects, err := git(ctx, path, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return err
	}
	for _, object := range strings.Fields(localObjects) {
		if _, ok := published[object]; !ok {
			return fmt.Errorf("old source contains unpublished object %s; replacement refused", object)
		}
	}
	return nil
}
