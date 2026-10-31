package fossillib

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Branch is a Fossil branch. A Fossil branch is a tag ("branch" property)
// carried by check-ins, not a separate pointer object. It may have more than
// one leaf; HeadId is the newest and HeadIds lists all of them.
type Branch struct {
	Name      string
	HeadId    string
	HeadIds   []string
	IsClosed  bool
	IsPrivate bool
	Comment   string
	Date      time.Time
}

// Tag is a Fossil symbolic tag. Fossil treats branch names as tags too, so
// IsBranch marks tags that name a branch.
type Tag struct {
	Name     string
	HeadId   string
	IsLocal  bool
	Rev      int
	IsBranch bool
	Value    string
	Date     time.Time
	Comment  string
}

var (
	reLeavesBranch = regexp.MustCompile(`^\*\*\*\s+(.*?)\s+\*\*\*\s*$`)
	reLeafLine     = regexp.MustCompile(`^\s*\(\d+\)\s+(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s+\[([0-9a-fA-F]+)\]\s+(.*)$`)
)

type leafRecord struct {
	hash    string
	date    time.Time
	comment string
}

// GetAllBranchList returns every branch, with all of its leaves. It uses one
// "fossil branch list" process for the names, one for closed branches and one
// "fossil leaves" process for the leaf list. Abbreviated leaf hashes are
// expanded with one timeline page, not one process per leaf.
func (r *LocalFossilRepository) GetAllBranchList() (map[string]*Branch, error) {
	r.ensureStamp()
	r.mu.RLock()
	if r.branches != nil {
		res := make(map[string]*Branch, len(r.branches))
		for k, v := range r.branches {
			cp := *v
			cp.HeadIds = append([]string(nil), v.HeadIds...)
			res[k] = &cp
		}
		r.mu.RUnlock()
		return res, nil
	}
	r.mu.RUnlock()

	entries, err := r.branchListText()
	if err != nil {
		return nil, err
	}
	closedNames, err := r.branchNames(true)
	if err != nil {
		return nil, err
	}
	closed := make(map[string]bool, len(closedNames))
	for _, name := range closedNames {
		closed[name] = true
	}
	allNames := make([]string, 0, len(entries))
	private := make(map[string]bool, len(entries))
	for _, entry := range entries {
		allNames = append(allNames, entry.Name)
		if entry.Private {
			private[entry.Name] = true
		}
	}

	leaves, err := r.readLeaves()
	if err != nil {
		return nil, err
	}
	prefixes := make([]string, 0, len(allNames))
	for _, recs := range leaves {
		for _, rec := range recs {
			prefixes = append(prefixes, rec.hash)
		}
	}
	resolved := r.resolveHashPrefixes(prefixes)

	res := make(map[string]*Branch, len(allNames))
	for _, name := range allNames {
		b := &Branch{Name: name, IsClosed: closed[name], IsPrivate: private[name]}
		recs := leaves[name]
		sort.Slice(recs, func(i, j int) bool { return recs[i].date.After(recs[j].date) })
		for _, rec := range recs {
			full := resolved[rec.hash]
			if full == "" {
				continue
			}
			if b.HeadId == "" {
				b.HeadId = full
			}
			if !containsString(b.HeadIds, full) {
				b.HeadIds = append(b.HeadIds, full)
			}
		}
		if b.HeadId == "" {
			// The leaves table can lag behind after a push. Ask Fossil to
			// resolve the branch name directly.
			if info, err := r.getInfo(name); err == nil {
				b.HeadId = info.Hash
				b.HeadIds = []string{info.Hash}
				b.Comment = info.Comment
				b.Date = info.Date
			}
		} else {
			// The newest leaf line already carries the comment and date,
			// so no per-branch fossil info call is needed.
			for _, rec := range recs {
				if resolved[rec.hash] == b.HeadId {
					b.Comment = rec.comment
					b.Date = rec.date
					break
				}
			}
		}
		res[name] = b
	}

	r.mu.Lock()
	r.branches = res
	r.mu.Unlock()
	return copyBranchMap(res), nil
}

func (r *LocalFossilRepository) branchNames(closed bool) ([]string, error) {
	args := []string{"branch", "list", "-a"}
	if closed {
		args = []string{"branch", "list", "-c"}
	}
	payload, err := r.runJSON(args...)
	if err != nil {
		return nil, err
	}
	var pl struct {
		Branches []string `json:"branches"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &pl); err != nil {
			return nil, fmt.Errorf("%w: fossil json branch list: %v", ErrInvalidFormat, err)
		}
	}
	return pl.Branches, nil
}

type branchListEntry struct {
	Name    string
	Private bool
}

func (r *LocalFossilRepository) branchListText() ([]branchListEntry, error) {
	out, err := r.run("branch", "list", "-a")
	if err != nil {
		return nil, err
	}
	res := make([]branchListEntry, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		name := strings.TrimSpace(line)
		entry := branchListEntry{}
		for len(name) > 0 && (name[0] == '*' || name[0] == '#') {
			if name[0] == '#' {
				entry.Private = true
			}
			name = strings.TrimSpace(name[1:])
		}
		entry.Name = strings.TrimSpace(name)
		if entry.Name != "" {
			res = append(res, entry)
		}
	}
	return res, nil
}

func (r *LocalFossilRepository) readLeaves() (map[string][]leafRecord, error) {
	out, err := r.run("leaves", "-a", "--bybranch", "-W", "0")
	if err != nil {
		return nil, err
	}
	res := make(map[string][]leafRecord)
	current := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := reLeavesBranch.FindStringSubmatch(line); len(m) > 0 {
			current = strings.TrimSpace(m[1])
			if current != "" {
				if _, ok := res[current]; !ok {
					res[current] = nil
				}
			}
			continue
		}
		if m := reLeafLine.FindStringSubmatch(line); len(m) > 0 {
			t, _ := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.UTC)
			res[current] = append(res[current], leafRecord{
				hash:    normalizeHash(m[2]),
				date:    t,
				comment: cleanLeafComment(m[3]),
			})
		}
	}
	return res, nil
}

func cleanLeafComment(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " (user: "); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func copyBranchMap(in map[string]*Branch) map[string]*Branch {
	res := make(map[string]*Branch, len(in))
	for k, v := range in {
		cp := *v
		cp.HeadIds = append([]string(nil), v.HeadIds...)
		res[k] = &cp
	}
	return res
}

func copyTagMap(in map[string]*Tag) map[string]*Tag {
	res := make(map[string]*Tag, len(in))
	for k, v := range in {
		cp := *v
		res[k] = &cp
	}
	return res
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SyncAllBranchList drops and reloads the cached branch list.
func (r *LocalFossilRepository) SyncAllBranchList() error {
	r.invalidateNames()
	_, err := r.GetAllBranchList()
	return err
}

// SyncBranch refreshes one branch entry.
func (r *LocalFossilRepository) SyncBranch(name string) error {
	r.invalidateNames()
	_, err := r.GetAllBranchList()
	return err
}

// CreateBranch creates a branch in advance. Fossil normally creates branches
// at commit time with "fossil commit --branch", which is the preferred idiom.
func (r *LocalFossilRepository) CreateBranch(name, basis string) error {
	if !validBranchName(name) {
		return fmt.Errorf("invalid branch name %q", name)
	}
	if basis == "" {
		tip, err := r.getTipHash()
		if err != nil {
			return err
		}
		basis = tip
	}
	args := []string{"branch", "new", name, basis}
	if _, err := r.runAsUser(args...); err != nil {
		return err
	}
	r.invalidateNames()
	return nil
}

// CloseBranch marks a branch closed. Closing a branch is a tag, not a
// deletion; the history remains.
func (r *LocalFossilRepository) CloseBranch(name string) error {
	if _, err := r.runAsUser("branch", "close", name); err != nil {
		return err
	}
	r.invalidateNames()
	return nil
}

// ReopenBranch removes the closed tag from a branch.
func (r *LocalFossilRepository) ReopenBranch(name string) error {
	if _, err := r.runAsUser("branch", "reopen", name); err != nil {
		return err
	}
	r.invalidateNames()
	return nil
}

// GetAllTagList returns all symbolic tags. Branch names appear as tags, as
// they do in Fossil itself; IsBranch distinguishes them.
func (r *LocalFossilRepository) GetAllTagList() (map[string]*Tag, error) {
	r.ensureStamp()
	r.mu.RLock()
	if r.tags != nil {
		res := copyTagMap(r.tags)
		r.mu.RUnlock()
		return res, nil
	}
	r.mu.RUnlock()

	out, err := r.run("tag", "list")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, 16)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	branchNames, _ := r.GetAllBranchList()
	branchSet := make(map[string]bool, len(branchNames))
	for name := range branchNames {
		branchSet[name] = true
	}

	hashes, _ := parallelMap(names, 8, func(name string) (string, error) {
		info, err := r.getInfo(name)
		if err != nil {
			return "", err
		}
		return info.Hash, nil
	})
	res := make(map[string]*Tag, len(names))
	for i, name := range names {
		hash := ""
		if i < len(hashes) {
			hash = hashes[i]
		}
		if hash == "" {
			// Tags on non-check-in artifacts (wiki pages, tickets) are not
			// useful to a code-hosting view; skip them instead of exposing
			// a tag with no check-in.
			continue
		}
		info, _ := r.getInfo(hash)
		if info != nil && info.Type != "" && info.Type != "checkin" {
			continue
		}
		t := &Tag{
			Name:     name,
			HeadId:   hash,
			IsBranch: branchSet[name],
		}
		if info != nil {
			t.Date = info.Date
			t.Comment = info.Comment
		}
		res[name] = t
	}

	r.mu.Lock()
	r.tags = res
	r.mu.Unlock()
	return copyTagMap(res), nil
}

// SyncAllTagList drops and reloads the cached tag list.
func (r *LocalFossilRepository) SyncAllTagList() error {
	r.invalidateNames()
	_, err := r.GetAllTagList()
	return err
}

// GetTagsAt returns the tags attached to one check-in.
func (r *LocalFossilRepository) GetTagsAt(c *CommitObject) (map[string]*Tag, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	payload, err := r.runJSON("tag", "list", "--checkin", c.Id)
	if err != nil {
		return nil, err
	}
	var pl struct {
		Tags map[string]*string `json:"tags"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &pl); err != nil {
			return nil, fmt.Errorf("%w: fossil json tag list: %v", ErrInvalidFormat, err)
		}
	}
	res := make(map[string]*Tag, len(pl.Tags))
	r.mu.RLock()
	branchNames := make(map[string]bool, len(r.branches))
	for name := range r.branches {
		branchNames[name] = true
	}
	r.mu.RUnlock()
	for name, value := range pl.Tags {
		t := &Tag{
			Name:     name,
			HeadId:   c.Id,
			Date:     c.CommitTime,
			IsBranch: branchNames[name],
		}
		if value != nil {
			t.Value = *value
		}
		res[name] = t
	}
	return res, nil
}

// AddTag adds a symbolic tag to an artifact.
func (r *LocalFossilRepository) AddTag(name, target, value string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid tag name %q", name)
	}
	args := []string{"tag", "add", name, target}
	if value != "" {
		args = append(args, value)
	}
	if _, err := r.runAsUser(args...); err != nil {
		return err
	}
	r.invalidateNames()
	return nil
}

// CancelTag removes a symbolic tag from an artifact.
func (r *LocalFossilRepository) CancelTag(name, target string) error {
	if _, err := r.runAsUser("tag", "cancel", name, target); err != nil {
		return err
	}
	r.invalidateNames()
	return nil
}

// FindTag returns the check-ins that currently carry a tag, oldest first.
func (r *LocalFossilRepository) FindTag(name string, limit int) ([]string, error) {
	rawName := name
	if !strings.HasPrefix(rawName, "sym-") {
		rawName = "sym-" + rawName
	}
	args := []string{"tag", "find", "--name", rawName, "--raw", "--type", "ci"}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprint(limit))
	}
	payload, err := r.runJSON(args...)
	if err != nil {
		return nil, err
	}
	var pl struct {
		Artifacts []string `json:"artifacts"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &pl); err != nil {
			return nil, fmt.Errorf("%w: fossil json tag find: %v", ErrInvalidFormat, err)
		}
	}
	for i := range pl.Artifacts {
		pl.Artifacts[i] = normalizeHash(pl.Artifacts[i])
	}
	return pl.Artifacts, nil
}

// GetBranch returns one branch by name.
func (r *LocalFossilRepository) GetBranch(name string) (*Branch, error) {
	branches, err := r.GetAllBranchList()
	if err != nil {
		return nil, err
	}
	b, ok := branches[name]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return b, nil
}
