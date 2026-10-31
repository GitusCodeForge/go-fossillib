package fossillib

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var reLsHashLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})\s+\S+\s+\[([0-9a-fA-F]+)\]\s+(.*)$`)

type lastCommitRecord struct {
	date time.Time
	hash string
}

// ResolveLastCommitAtDir returns, for every direct entry of dir at c, the
// check-in hash of the newest change to it. For a file that is the check-in
// that last changed the file; for a directory it is the newest check-in that
// changed anything below it.
//
// This is one "fossil ls -H" process for the whole tree, then one short
// "fossil info" call per entry whose abbreviated hash is not already cached.
// Results are memoized in memory per handle.
func (r *LocalFossilRepository) ResolveLastCommitAtDir(c *CommitObject, dir string) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	dir = normalizeManifestPath(dir)
	key := c.Id + "\x00" + dir
	r.mu.RLock()
	if cached, ok := r.lastCommit[key]; ok {
		res := make(map[string]string, len(cached))
		for k, v := range cached {
			res[k] = v
		}
		r.mu.RUnlock()
		return res, nil
	}
	r.mu.RUnlock()

	m, err := r.loadManifest(c.Id)
	if err != nil {
		return nil, err
	}
	children := m.Children(dir)
	if len(children) == 0 {
		return map[string]string{}, nil
	}

	out, err := r.run("ls", "-r", c.Id, "-H")
	if err != nil {
		return nil, err
	}
	files := make(map[string]lastCommitRecord)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		m := reLsHashLine.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		t, _ := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+m[2], time.UTC)
		files[m[4]] = lastCommitRecord{date: t, hash: normalizeHash(m[3])}
	}

	chosen := make(map[string]lastCommitRecord, len(children))
	for _, item := range children {
		var rec lastCommitRecord
		found := false
		if item.IsDir {
			prefix := item.Name + "/"
			if dir != "" {
				prefix = dir + "/" + item.Name + "/"
			}
			for path, candidate := range files {
				if !strings.HasPrefix(path, prefix) {
					continue
				}
				if !found || candidate.date.After(rec.date) ||
					(candidate.date.Equal(rec.date) && candidate.hash < rec.hash) {
					rec = candidate
					found = true
				}
			}
		} else {
			path := item.Name
			if dir != "" {
				path = dir + "/" + item.Name
			}
			rec, found = files[path]
		}
		if found {
			chosen[item.Name] = rec
		}
	}
	prefixes := make([]string, 0, len(chosen))
	for _, rec := range chosen {
		prefixes = append(prefixes, rec.hash)
	}
	resolved := r.resolveHashPrefixes(prefixes)
	res := make(map[string]string, len(chosen))
	for name, rec := range chosen {
		if full := resolved[rec.hash]; full != "" {
			res[name] = full
		}
	}

	r.mu.Lock()
	if len(r.lastCommit) > 256 {
		r.lastCommit = make(map[string]map[string]string)
	}
	r.lastCommit[key] = res
	r.mu.Unlock()
	cp := make(map[string]string, len(res))
	for k, v := range res {
		cp[k] = v
	}
	return cp, nil
}

// ClearLastCommitCache drops the in-memory last-commit memoization.
func (r *LocalFossilRepository) ClearLastCommitCache() {
	r.mu.Lock()
	r.lastCommit = make(map[string]map[string]string)
	r.mu.Unlock()
}
