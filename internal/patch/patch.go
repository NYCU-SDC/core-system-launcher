// Package patch fetches the upstream repos at pinned commits and applies the
// changes under patches/.
//
// Patches rather than a fork: the three core system repos are a memorial
// project and these changes are deliberately never committed back. Keeping them
// as patches makes both what changed and which commit it applies to obvious.
package patch

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Repo is an upstream repository to fetch.
type Repo struct {
	Name   string // backend / frontend
	URL    string
	Commit string
}

// Versions parses the pinned commit of each repo out of versions.lock.
func Versions(lock []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(lock)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if out["backend"] == "" || out["frontend"] == "" {
		return nil, fmt.Errorf("versions.lock 缺少 backend 或 frontend 的 commit")
	}
	return out, nil
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s 失敗：%s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// Fetch checks the repo out at the given commit.
//
// init plus a single-commit fetch rather than a full clone: these repos have
// hundreds of commits and only one is needed, and it avoids implying the result
// is a clone anyone should develop in. Already being on the right commit is
// reported back so repeated runs stay cheap.
func Fetch(ctx context.Context, r Repo, dest string) (skipped bool, err error) {
	if _, statErr := os.Stat(filepath.Join(dest, ".git")); statErr == nil {
		cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
		cmd.Dir = dest
		out, revErr := cmd.Output()
		if revErr == nil && strings.TrimSpace(string(out)) == r.Commit {
			return true, nil
		}
		// Wrong commit, or the tree was tampered with. Starting over is simpler.
		if err := os.RemoveAll(dest); err != nil {
			return false, err
		}
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return false, err
	}
	if err := git(ctx, dest, "init", "-q"); err != nil {
		return false, err
	}
	if err := git(ctx, dest, "remote", "add", "origin", r.URL); err != nil {
		return false, err
	}
	if err := git(ctx, dest, "fetch", "-q", "--depth", "1", "origin", r.Commit); err != nil {
		return false, fmt.Errorf("取得 %s 的 commit %.8s 失敗，可能是網路問題或該 commit 已不存在：%w",
			r.Name, r.Commit, err)
	}
	return false, git(ctx, dest, "checkout", "-q", "FETCH_HEAD")
}

// List returns the sorted patch filenames for a repo. patches is the embedded
// filesystem rooted at the patches/ directory.
func List(patches fs.FS, repo string) ([]string, error) {
	entries, err := fs.ReadDir(patches, repo)
	if err != nil {
		// A missing directory just means this repo has no changes.
		return nil, nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".patch") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Apply applies a repo's patches in order and returns the ones applied.
//
// Each is verified with --check first so a conflict never leaves a half-patched
// working tree behind.
func Apply(ctx context.Context, patches fs.FS, repo, dest string) ([]string, error) {
	names, err := List(patches, repo)
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, name := range names {
		data, err := fs.ReadFile(patches, path.Join(repo, name))
		if err != nil {
			return applied, err
		}
		tmp, err := os.CreateTemp("", "cs-patch-*.patch")
		if err != nil {
			return applied, err
		}
		_, writeErr := tmp.Write(data)
		closeErr := tmp.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(tmp.Name())
			return applied, fmt.Errorf("寫入暫存 patch 失敗：%v %v", writeErr, closeErr)
		}

		if err := gitApply(ctx, dest, tmp.Name(), true); err != nil {
			_ = os.Remove(tmp.Name())
			return applied, fmt.Errorf("patch %s 套用不上去。\n"+
				"    多半是 versions.lock 釘的 commit 和 patch 內容對不起來了。\n"+
				"    %w", name, err)
		}
		err = gitApply(ctx, dest, tmp.Name(), false)
		_ = os.Remove(tmp.Name())
		if err != nil {
			return applied, fmt.Errorf("patch %s 套用失敗：%w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

func gitApply(ctx context.Context, dir, patchPath string, check bool) error {
	args := []string{"apply"}
	if check {
		args = append(args, "--check")
	}
	args = append(args, patchPath)
	return git(ctx, dir, args...)
}
