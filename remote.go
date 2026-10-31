package fossillib

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CloneFossilRepository clones a remote repository into a new file. uri may
// be an http(s), ssh or filesystem URL. The result is not opened as a
// check-out; use NewLocalFossilRepository to read it.
func CloneFossilRepository(uri, path string) (*LocalFossilRepository, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := runFossilIn(filepath.Dir(abs), "clone", uri, abs, "--no-open"); err != nil {
		return nil, err
	}
	return NewLocalFossilRepository(abs)
}

// remoteEntry is one line of "fossil remote list".
type remoteEntry struct {
	Name string
	URL  string
}

func (r *LocalFossilRepository) readRemoteList() ([]remoteEntry, error) {
	out, err := r.run("remote", "list")
	if err != nil {
		return nil, err
	}
	res := make([]remoteEntry, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		res = append(res, remoteEntry{Name: fields[0], URL: strings.Join(fields[1:], " ")})
	}
	return res, nil
}

func (r *LocalFossilRepository) defaultRemoteURL() (string, error) {
	out, err := r.run("remote")
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(string(out))
	if url == "off" {
		return "", nil
	}
	return url, nil
}

// GetAllRemote returns the configured remote names. Fossil's unnamed default
// remote is reported as "default".
func (r *LocalFossilRepository) GetAllRemote() ([]string, error) {
	entries, err := r.readRemoteList()
	if err != nil {
		return nil, err
	}
	res := make([]string, 0, len(entries)+1)
	if url, err := r.defaultRemoteURL(); err == nil && url != "" {
		res = append(res, "default")
	}
	for _, e := range entries {
		res = append(res, e.Name)
	}
	return res, nil
}

// HasRemote reports whether a remote name is configured.
func (r *LocalFossilRepository) HasRemote(name string) (bool, error) {
	_, err := r.GetRemoteURL(name)
	if err == nil {
		return true, nil
	}
	if err == ErrObjectNotFound {
		return false, nil
	}
	// A "not found" from fossil arrives as a CommandError rather than our
	// sentinel.
	if isNotFoundError(err) {
		return false, nil
	}
	return false, err
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, needle := range []string{
		"not found",
		"no such",
		"unknown remote",
		"unknown branch",
		"unknown tag",
		"cannot find",
		"cannot resolve",
		"not a valid",
		"invalid check-in",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// GetRemoteURL returns the URL of a remote. The name "default" refers to the
// unnamed remote Fossil remembers from the last clone/push/pull.
func (r *LocalFossilRepository) GetRemoteURL(name string) (string, error) {
	if name == "" || name == "default" {
		url, err := r.defaultRemoteURL()
		if err != nil {
			return "", err
		}
		if url == "" {
			return "", fmt.Errorf("%w: default remote", ErrObjectNotFound)
		}
		return url, nil
	}
	entries, err := r.readRemoteList()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.URL, nil
		}
	}
	return "", fmt.Errorf("%w: remote %q", ErrObjectNotFound, name)
}

// SetRemote adds or replaces a named remote. The name "default" sets the
// unnamed remote.
func (r *LocalFossilRepository) SetRemote(name, url string) error {
	if url == "" {
		return fmt.Errorf("empty remote URL")
	}
	if name == "" || name == "default" {
		_, err := r.run("remote", url)
		return err
	}
	if _, err := r.run("remote", "add", name, url); err != nil {
		// "remote add" refuses an existing name. Replace it explicitly so
		// that updating a named remote never changes the default remote.
		if _, delErr := r.run("remote", "delete", name); delErr == nil {
			_, addErr := r.run("remote", "add", name, url)
			return addErr
		}
		return err
	}
	return nil
}

// DeleteRemote removes a named remote or turns the default remote off.
func (r *LocalFossilRepository) DeleteRemote(name string) error {
	if name == "" || name == "default" {
		_, err := r.run("remote", "off")
		return err
	}
	_, err := r.run("remote", "delete", name)
	return err
}

// FetchRemote pulls from a remote.
func (r *LocalFossilRepository) FetchRemote(name string) error {
	args := []string{"pull"}
	if name != "" && name != "default" {
		args = append(args, name)
	}
	_, err := r.run(args...)
	r.invalidateAfterWrite()
	return err
}

// PushRemote pushes to a remote.
func (r *LocalFossilRepository) PushRemote(name string) error {
	args := []string{"push"}
	if name != "" && name != "default" {
		args = append(args, name)
	}
	_, err := r.run(args...)
	return err
}

// SyncRemote synchronizes both directions with a remote.
func (r *LocalFossilRepository) SyncRemote(name string) error {
	args := []string{"sync"}
	if name != "" && name != "default" {
		args = append(args, name)
	}
	_, err := r.run(args...)
	r.invalidateAfterWrite()
	return err
}

// GetRemote is an alias for GetRemoteURL kept for parity with other SCM
// helpers.
func (r *LocalFossilRepository) GetRemote(name string) (string, error) {
	return r.GetRemoteURL(name)
}
