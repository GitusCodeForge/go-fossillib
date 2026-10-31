package fossillib

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// AuthorTime is a Fossil user name and, when the login string contains one, an
// email address. Fossil records a single user per artifact; AuthorInfo and
// CommitterInfo are therefore always the same value.
type AuthorTime struct {
	AuthorName  string
	AuthorEmail string
	Time        time.Time
}

func (at *AuthorTime) String() string {
	if at == nil {
		return ""
	}
	if at.AuthorName == "" {
		return fmt.Sprintf("<%s>", at.AuthorEmail)
	}
	if at.AuthorEmail == "" {
		return at.AuthorName
	}
	return fmt.Sprintf("%s <%s>", at.AuthorName, at.AuthorEmail)
}

// FileChange describes one path changed by a check-in.
type FileChange struct {
	Path         string `json:"path"`
	OldPath      string `json:"oldPath,omitempty"`
	BlobId       string `json:"blobId"`
	ParentBlobId string `json:"parentBlobId,omitempty"`
	Size         int64  `json:"size,omitempty"`
	// State is Fossil's own vocabulary: "added", "modified", "removed" or
	// "renamed". Timeline data uses the same words.
	State string `json:"state"`
	Mode  int    `json:"mode,omitempty"`
	Flags string `json:"flags,omitempty"`
}

// CommitObject is a Fossil check-in. The check-in artifact itself is also
// the complete manifest (or a delta manifest), so TreeObjId is the check-in
// hash.
//
// Fossil has no revision numbers. Rev, P1 and P2 are always -1; they exist so
// that code ported from the Git and Mercurial helpers keeps compiling.
type CommitObject struct {
	Id            string
	Rev           int
	TreeObjId     string
	ParentIdList  []string
	P1            int
	P2            int
	AuthorInfo    AuthorTime
	CommitterInfo AuthorTime
	CoAuthorInfo  []AuthorTime
	CommitTime    time.Time
	CommitMessage string
	Branch        string
	Tags          []string
	Files         []string
	FileChanges   []FileChange
	Extra         map[string]string
	Signature     string

	rawData       []byte
	changesFilled uint32
}

func (c *CommitObject) ParentId() string {
	if c == nil || len(c.ParentIdList) == 0 {
		return ""
	}
	return c.ParentIdList[0]
}

func (c *CommitObject) IsMerge() bool {
	return c != nil && len(c.ParentIdList) > 1
}

// IsPartial reports whether the commit was built from timeline data and its
// raw artifact has not been fetched yet. Call GetCommit to upgrade it.
func (c *CommitObject) IsPartial() bool { return c != nil && c.rawData == nil }

func (c *CommitObject) Type() FossilObjectType { return CHECKIN }
func (c *CommitObject) ObjectId() string       { return c.Id }
func (c *CommitObject) RawData() []byte        { return c.rawData }

func (c *CommitObject) String() string {
	return fmt.Sprintf("Commit{%s,%s}", c.Id, c.Branch)
}

var reCoAuthoredBy = regexp.MustCompile(`^\s*[cC]o-[aA]uthored-[bB]y:\s*([^<>]+)\s*<([^>]*)>\s*$`)

func parseFossilUser(s string) AuthorTime {
	s = strings.TrimSpace(s)
	res := AuthorTime{}
	if i := strings.LastIndexByte(s, '<'); i >= 0 && strings.HasSuffix(s, ">") {
		res.AuthorName = strings.TrimSpace(s[:i])
		res.AuthorEmail = strings.TrimSpace(s[i+1 : len(s)-1])
		return res
	}
	// A bare Fossil login is common in older projects.
	res.AuthorName = s
	return res
}

func commitFromArtifact(art *Artifact) *CommitObject {
	c := &CommitObject{
		Id:            art.Id,
		Rev:           -1,
		TreeObjId:     art.Id,
		P1:            -1,
		P2:            -1,
		ParentIdList:  append([]string(nil), art.Parents...),
		CommitMessage: art.Comment,
		Extra:         make(map[string]string),
		Signature:     art.Signature,
		rawData:       art.Raw,
	}
	c.AuthorInfo = parseFossilUser(art.User)
	c.AuthorInfo.Time = art.Date
	c.CommitterInfo = c.AuthorInfo
	c.CommitTime = art.Date

	tagSet := make(map[string]bool)
	for _, tc := range art.Tags {
		if tc.Target != "*" {
			continue
		}
		switch {
		case tc.Name == "branch":
			if tc.HasValue && tc.Op != '-' {
				c.Branch = tc.Value
			}
		case strings.HasPrefix(tc.Name, "sym-"):
			name := strings.TrimPrefix(tc.Name, "sym-")
			if name == "" {
				continue
			}
			if tc.Op == '-' {
				delete(tagSet, name)
			} else {
				tagSet[name] = true
			}
		case tc.Name == "comment" && tc.HasValue:
			c.Extra["comment-override"] = tc.Value
		case tc.Name == "user" && tc.HasValue:
			c.Extra["user-override"] = tc.Value
		case tc.Name == "date" && tc.HasValue:
			c.Extra["date-override"] = tc.Value
		}
	}
	if c.Branch == "" {
		c.Branch = "trunk"
	}
	c.Extra["branch"] = c.Branch
	c.Tags = make([]string, 0, len(tagSet)+1)
	c.Tags = append(c.Tags, c.Branch)
	for name := range tagSet {
		if name != c.Branch {
			c.Tags = append(c.Tags, name)
		}
	}
	sort.Strings(c.Tags)

	for _, line := range strings.Split(c.CommitMessage, "\n") {
		m := reCoAuthoredBy.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) == 0 {
			continue
		}
		name := strings.TrimSpace(m[1])
		email := strings.TrimSpace(m[2])
		if authorTimeExists(c.CoAuthorInfo, name, email) {
			continue
		}
		c.CoAuthorInfo = append(c.CoAuthorInfo, AuthorTime{
			AuthorName:  name,
			AuthorEmail: email,
			Time:        c.AuthorInfo.Time,
		})
	}
	return c
}

func authorTimeExists(list []AuthorTime, name, email string) bool {
	for _, v := range list {
		if v.AuthorName == name && v.AuthorEmail == email {
			return true
		}
	}
	return false
}

// fillChanges compares the check-in manifest with the primary parent's
// manifest. Comparing manifests is exact, does not depend on the repository
// schema, and reuses the manifest cache.
func (r *LocalFossilRepository) fillChanges(c *CommitObject) error {
	if c == nil {
		return fmt.Errorf("no check-in given")
	}
	cur, err := r.loadManifest(c.Id)
	if err != nil {
		return err
	}
	var parent *ManifestObject
	if len(c.ParentIdList) > 0 {
		parent, err = r.loadManifest(c.ParentIdList[0])
		if err != nil {
			return err
		}
	}
	parentIndex := make(map[string]*ManifestItem)
	if parent != nil {
		for i := range parent.ItemList {
			item := &parent.ItemList[i]
			parentIndex[item.Path] = item
		}
	}
	changes := make([]FileChange, 0, 8)
	for i := range cur.ItemList {
		item := &cur.ItemList[i]
		old, existed := parentIndex[item.Path]
		if existed && old.BlobId == item.BlobId && old.Mode == item.Mode && old.Flags == item.Flags {
			delete(parentIndex, item.Path)
			continue
		}
		fc := FileChange{
			Path:    item.Path,
			BlobId:  item.BlobId,
			Mode:    item.Mode,
			Flags:   item.Flags,
			OldPath: item.OldPath,
			State:   "modified",
		}
		if existed {
			fc.ParentBlobId = old.BlobId
			delete(parentIndex, item.Path)
		} else if item.OldPath != "" {
			if oldItem, ok := parentIndex[item.OldPath]; ok {
				fc.ParentBlobId = oldItem.BlobId
				fc.State = "renamed"
				delete(parentIndex, item.OldPath)
			} else {
				fc.State = "added"
			}
		} else {
			fc.State = "added"
		}
		changes = append(changes, fc)
	}
	for _, item := range parentIndex {
		changes = append(changes, FileChange{
			Path:   item.Path,
			BlobId: item.BlobId,
			Mode:   item.Mode,
			Flags:  item.Flags,
			State:  "removed",
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	c.FileChanges = changes
	c.Files = make([]string, len(changes))
	for i := range changes {
		c.Files[i] = changes[i].Path
	}
	return nil
}

// GetChangedFiles returns the changed-file list of a check-in, computing it
// from the manifests if the commit was built from timeline data.
func (r *LocalFossilRepository) GetChangedFiles(c *CommitObject) ([]FileChange, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	if atomic.LoadUint32(&c.changesFilled) == 0 {
		r.fillMu.Lock()
		defer r.fillMu.Unlock()
		if atomic.LoadUint32(&c.changesFilled) == 0 {
			if err := r.fillChanges(c); err != nil {
				return nil, err
			}
			atomic.StoreUint32(&c.changesFilled, 1)
		}
	}
	return append([]FileChange(nil), c.FileChanges...), nil
}
