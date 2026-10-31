package fossillib

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// timelinePayload mirrors the payload of "fossil json timeline checkin".
type timelinePayload struct {
	Limit    int             `json:"limit"`
	Branch   string          `json:"branch"`
	Tag      string          `json:"tag"`
	Timeline []timelineEntry `json:"timeline"`
}

type timelineEntry struct {
	Type      string         `json:"type"`
	UUID      string         `json:"uuid"`
	IsLeaf    bool           `json:"isLeaf"`
	Timestamp int64          `json:"timestamp"`
	User      string         `json:"user"`
	Comment   string         `json:"comment"`
	Parents   []string       `json:"parents"`
	Tags      []string       `json:"tags"`
	Files     []timelineFile `json:"files"`
}

type timelineFile struct {
	Name         string `json:"name"`
	UUID         string `json:"uuid"`
	Parent       string `json:"parent"`
	Size         int64  `json:"size"`
	State        string `json:"state"`
	DownloadPath string `json:"downloadPath"`
}

func commitFromTimeline(e timelineEntry, branchHint string) *CommitObject {
	c := &CommitObject{
		Id:            normalizeHash(e.UUID),
		Rev:           -1,
		TreeObjId:     normalizeHash(e.UUID),
		P1:            -1,
		P2:            -1,
		ParentIdList:  append([]string(nil), e.Parents...),
		CommitMessage: e.Comment,
		Tags:          append([]string(nil), e.Tags...),
		Branch:        branchHint,
		Extra:         make(map[string]string),
	}
	c.AuthorInfo = parseFossilUser(e.User)
	c.AuthorInfo.Time = time.Unix(e.Timestamp, 0).UTC()
	c.CommitterInfo = c.AuthorInfo
	c.CommitTime = c.AuthorInfo.Time
	if len(c.ParentIdList) > 1 {
		c.P2 = -1
	}
	c.Extra["branch"] = c.Branch
	atomic.StoreUint32(&c.changesFilled, 1)
	for _, f := range e.Files {
		fc := FileChange{
			Path:    f.Name,
			BlobId:  normalizeHash(f.UUID),
			Size:    f.Size,
			State:   f.State,
			OldPath: "",
		}
		if f.Parent != "" {
			fc.ParentBlobId = normalizeHash(f.Parent)
		}
		c.FileChanges = append(c.FileChanges, fc)
		c.Files = append(c.Files, f.Name)
	}
	for _, line := range splitLinesString(c.CommitMessage) {
		m := reCoAuthoredBy.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		name := trimSpace(m[1])
		email := trimSpace(m[2])
		if authorTimeExists(c.CoAuthorInfo, name, email) {
			continue
		}
		c.CoAuthorInfo = append(c.CoAuthorInfo, AuthorTime{
			AuthorName:  name,
			AuthorEmail: email,
			Time:        c.CommitTime,
		})
	}
	return c
}

func splitLinesString(s string) []string {
	var res []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			res = append(res, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		res = append(res, s[start:])
	}
	return res
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// listBranchCommits fetches a page of check-ins for a branch in one fossil
// process. n <= 0 means no limit.
func (r *LocalFossilRepository) listBranchCommits(branch string, n int) ([]CommitObject, error) {
	args := []string{"timeline", "checkin", "-f"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	if n > 0 {
		args = append(args, "-n", strconv.Itoa(n))
	} else {
		args = append(args, "-n", "0")
	}
	payload, err := r.runJSON(args...)
	if err != nil {
		return nil, err
	}
	var tl timelinePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &tl); err != nil {
			return nil, fmt.Errorf("%w: fossil json timeline: %v", ErrInvalidFormat, err)
		}
	}
	branchSet := make(map[string]bool)
	r.mu.RLock()
	if r.branches != nil {
		for name := range r.branches {
			branchSet[name] = true
		}
	}
	r.mu.RUnlock()
	res := make([]CommitObject, 0, len(tl.Timeline))
	for _, e := range tl.Timeline {
		actual := branch
		matches := make([]string, 0, 2)
		for _, tag := range e.Tags {
			if branchSet[tag] {
				matches = append(matches, tag)
			}
		}
		if branch != "" && containsString(matches, branch) {
			actual = branch
		} else if len(matches) == 1 {
			actual = matches[0]
		} else if actual == "" && len(matches) > 0 {
			actual = matches[0]
		}
		c := commitFromTimeline(e, actual)
		r.mu.Lock()
		if old := r.commits[c.Id]; old == nil {
			r.commits[c.Id] = c
		}
		r.mu.Unlock()
		res = append(res, *c)
	}
	return res, nil
}

// firstParentChain filters a branch timeline page down to the first-parent
// chain that starts at head. It reports whether the whole chain was present
// in the page; a false result means the caller must walk further with
// artifact fetches.
func firstParentChain(page []CommitObject, head string, n int) ([]CommitObject, bool) {
	index := make(map[string]int, len(page))
	for i := range page {
		index[page[i].Id] = i
	}
	res := make([]CommitObject, 0, len(page))
	cur := head
	for cur != "" {
		i, ok := index[cur]
		if !ok {
			return res, false
		}
		res = append(res, page[i])
		if n > 0 && len(res) >= n {
			return res, true
		}
		if len(page[i].ParentIdList) == 0 {
			return res, true
		}
		cur = page[i].ParentIdList[0]
	}
	return res, true
}

// GetCommitHistory walks first parents from cid. n <= 0 means no limit.
//
// The branch timeline is fetched with one "fossil json timeline" call and
// filtered to cid's first-parent chain. If cid is older than that page, the
// remaining check-ins are fetched as artifacts.
func (r *LocalFossilRepository) GetCommitHistory(cid string) ([]CommitObject, error) {
	return r.GetCommitHistoryN(cid, 0)
}

// GetCommitHistoryN is GetCommitHistory with a limit.
func (r *LocalFossilRepository) GetCommitHistoryN(cid string, n int) ([]CommitObject, error) {
	full, err := r.resolveHash(cid)
	if err != nil {
		return nil, err
	}
	head, err := r.loadCommit(full, false)
	if err != nil {
		return nil, err
	}
	if head.Branch != "" {
		if page, err := r.listBranchCommits(head.Branch, n); err == nil {
			if chain, complete := firstParentChain(page, full, n); complete && len(chain) > 0 {
				return chain, nil
			}
		}
	}

	limitless := n <= 0
	res := make([]CommitObject, 0, 16)
	cur := full
	for cur != "" {
		c, err := r.loadCommit(cur, true)
		if err != nil {
			return nil, err
		}
		res = append(res, *c)
		if !limitless {
			n--
			if n <= 0 {
				break
			}
		}
		if len(c.ParentIdList) == 0 {
			break
		}
		cur = c.ParentIdList[0]
	}
	return res, nil
}

// GetBranchCommitHistory returns the history reachable through first parents
// from the branch head.
func (r *LocalFossilRepository) GetBranchCommitHistory(b Branch) ([]CommitObject, error) {
	return r.GetBranchCommitHistoryN(b, 0)
}

// GetBranchCommitHistoryN is GetBranchCommitHistory with a limit.
func (r *LocalFossilRepository) GetBranchCommitHistoryN(b Branch, n int) ([]CommitObject, error) {
	if b.Name != "" && b.HeadId != "" {
		if page, err := r.listBranchCommits(b.Name, n); err == nil {
			if chain, complete := firstParentChain(page, b.HeadId, n); complete && len(chain) > 0 {
				return chain, nil
			}
		}
	}
	if b.HeadId != "" {
		return r.GetCommitHistoryN(b.HeadId, n)
	}
	if b.Name != "" {
		return r.listBranchCommits(b.Name, n)
	}
	return nil, fmt.Errorf("branch has no name or head")
}

// GetAllChangesets returns every check-in reachable from the repository tip,
// newest first.
func (r *LocalFossilRepository) GetAllChangesets() ([]*CommitObject, error) {
	// Populate the branch cache first so that timeline entries can be
	// labelled with their real branch, including merged-in commits.
	if _, err := r.GetAllBranchList(); err != nil {
		return nil, err
	}
	list, err := r.listBranchCommits("", 0)
	if err != nil {
		return nil, err
	}
	res := make([]*CommitObject, 0, len(list))
	for i := range list {
		res = append(res, &list[i])
	}
	return res, nil
}
