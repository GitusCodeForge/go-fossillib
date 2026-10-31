package fossillib

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// AddFileToRepoString commits one file to a branch. Fossil cannot commit
// without a check-out, so the function opens a temporary --keep check-out at
// the branch head (which sets the version without writing the whole tree),
// writes only the files that change, and commits exactly those paths.
//
// A branch that does not exist yet is created at the repository tip, matching
// the usual Fossil idiom of creating a branch at commit time. The returned
// value is the new check-in hash.
func (r *LocalFossilRepository) AddFileToRepoString(
	branchName string, filePath string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content string,
) (string, error) {
	return r.addFilesToRepo(branchName, map[string]string{filePath: content},
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

// AddFileToRepoReader is AddFileToRepoString for a stream. contentSize is
// accepted for symmetry with the Git and Mercurial helpers; Fossil does not
// need it.
func (r *LocalFossilRepository) AddFileToRepoReader(
	branchName string, filePath string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content io.Reader, contentSize int64,
) (string, error) {
	if !validRepoPath(filePath) {
		return "", fmt.Errorf("invalid file path %q", filePath)
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return "", err
	}
	return r.addFilesToRepo(branchName, map[string]string{filePath: string(data)},
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

// AddMultipleFileToRepoString commits several files at once. Paths are
// repository-root-relative.
func (r *LocalFossilRepository) AddMultipleFileToRepoString(
	branchName string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content map[string]string,
) (string, error) {
	return r.addFilesToRepo(branchName, content,
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

func (r *LocalFossilRepository) addFilesToRepo(
	branchName string,
	files map[string]string,
	authorName, authorEmail, committerName, committerEmail, commitMessage string,
) (string, error) {
	if !validBranchName(branchName) {
		return "", fmt.Errorf("invalid branch name %q", branchName)
	}
	if len(files) == 0 {
		return "", errors.New("no files to commit")
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		if !validRepoPath(p) {
			return "", fmt.Errorf("invalid file path %q", p)
		}
		paths = append(paths, p)
	}
	for _, s := range []string{authorName, authorEmail, committerName, committerEmail} {
		if !validUserPart(s) {
			return "", errors.New("commit metadata contains a newline")
		}
	}
	if strings.ContainsRune(commitMessage, 0) {
		return "", errors.New("commit message contains a NUL byte")
	}
	user := formatFossilUser(authorName, authorEmail)
	if user == "" {
		user = formatFossilUser(committerName, committerEmail)
	}
	if user == "" {
		return "", errors.New("no commit user")
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		base, exists, err := r.resolveBranchHead(branchName)
		if err != nil {
			return "", err
		}
		createBranch := !exists
		if !exists {
			base, err = r.getTipHash()
			if err != nil {
				return "", err
			}
		}

		var newHash string
		err = r.withTempCheckout(base, true, func(dir string) error {
			baseManifest, err := r.loadManifest(base)
			if err != nil {
				return err
			}
			for _, path := range paths {
				item, tracked := baseManifest.Get(path)
				mode := FOSSIL_MODE_NORMAL_FILE
				if tracked {
					mode = item.Mode
				}
				if err := writeCheckoutFile(dir, path, []byte(files[path]), mode); err != nil {
					return err
				}
				if !tracked {
					if _, err := runInCheckout(dir, "add", path); err != nil {
						return err
					}
				}
			}
			args := []string{"commit"}
			args = append(args, paths...)
			args = append(args, "-m", commitMessage,
				"--user-override", user,
				"--no-warnings",
				"--no-prompt",
				"--no-verify-comment",
				"--nosync")
			if createBranch {
				args = append(args, "--branch", branchName)
			}
			r.mu.RLock()
			login := r.CommitUser
			r.mu.RUnlock()
			if login != "" {
				args = append(args, "--user", login)
			}
			out, err := runInCheckout(dir, args...)
			if err != nil && isUserResolutionError(err) {
				if login = r.chooseCommitUser(); login != "" {
					retry := append(append([]string(nil), args...), "--user", login)
					out, err = runInCheckout(dir, retry...)
				}
			}
			if err != nil {
				return err
			}
			m := reNewVersion.FindSubmatch(out)
			if len(m) < 2 {
				return fmt.Errorf("fossil commit did not report New_Version: %s", strings.TrimSpace(string(out)))
			}
			newHash = normalizeHash(string(m[1]))
			return nil
		})
		if err == nil {
			r.invalidateAfterWrite()
			return newHash, nil
		}
		lastErr = err
		if !isRetryableCommitError(err) {
			return "", err
		}
		r.invalidateAfterWrite()
	}
	return "", lastErr
}

// isRetryableCommitError reports whether a commit failed because the branch
// head moved between reading it and committing.
func isRetryableCommitError(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	text := strings.ToLower(ce.Stdout + "\n" + ce.Stderr)
	for _, needle := range []string{
		"would fork",
		"use --allow-fork",
		"an open branch named",
		"branch has moved",
		"repository is locked",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
