package fossillib

import (
	"fmt"
	"os"
	"path/filepath"
)

// withTempCheckout opens a temporary check-out of the repository and runs fn
// in it. keep=false materializes the whole tree, which is required for merge;
// keep=true sets the check-out version without writing files, so the caller
// can materialize only the few files it needs with "fossil update".
func (r *LocalFossilRepository) withTempCheckout(version string, keep bool, fn func(dir string) error) error {
	dir, err := os.MkdirTemp("", "go-fossillib-checkout-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	args := []string{"open", r.RepositoryPath}
	if version != "" {
		args = append(args, version)
	}
	args = append(args, "--workdir", dir, "--nosync")
	if keep {
		args = append(args, "--keep")
	}
	if _, err := runInCheckout(dir, args...); err != nil {
		return err
	}
	return fn(dir)
}

// materializeFiles updates only the named files in a --keep check-out. It is
// used before annotating or deleting a file, where the content must exist but
// the rest of the tree is irrelevant.
func (r *LocalFossilRepository) materializeFiles(dir, version string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"update", version, "--nosync"}
	args = append(args, paths...)
	r.mu.RLock()
	login := r.CommitUser
	r.mu.RUnlock()
	if login != "" {
		args = append(args, "--user", login)
	}
	_, err := runInCheckout(dir, args...)
	if err != nil && isUserResolutionError(err) {
		if login = r.chooseCommitUser(); login != "" {
			retry := append(append([]string(nil), args...), "--user", login)
			_, err = runInCheckout(dir, retry...)
		}
	}
	return err
}

// writeCheckoutFile writes content below dir, creating parent directories and
// preserving the executable bit when the base manifest marked the file
// executable.
func writeCheckoutFile(dir, path string, content []byte, mode int) error {
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	perm := os.FileMode(0644)
	if mode&0111 != 0 {
		perm = 0755
	}
	if err := os.WriteFile(full, content, perm); err != nil {
		return err
	}
	return os.Chmod(full, perm)
}
