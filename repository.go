package fossillib

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LocalFossilRepository is a handle on a Fossil repository file. It never
// reads the repository's SQLite tables; all data comes from the fossil
// executable, so changes to the internal schema do not affect this package.
type LocalFossilRepository struct {
	// RepositoryPath is the absolute path of the .fossil repository file.
	RepositoryPath string
	// CheckoutPath is set when the handle was opened from a check-out
	// directory instead of a repository file.
	CheckoutPath string
	// Description mirrors the project description once project info has
	// been loaded.
	Description string
	// CommitUser is the Fossil login used for permission checks when the
	// library writes. Leave it empty to use Fossil's normal user resolution;
	// if that fails, a login with check-in capability is chosen.
	CommitUser string

	mu                 sync.RWMutex
	fillMu             sync.Mutex
	resolvedCommitUser string
	hashPolicy         int
	stamp              repoStamp
	commits            map[string]*CommitObject
	manifests          map[string]*ManifestObject
	infoByName         map[string]*commitInfo
	prefixHashes       map[string]string
	branches           map[string]*Branch
	tags               map[string]*Tag
	project            *ProjectInfo
	lastCommit         map[string]map[string]string
}

type repoStamp struct {
	modTime time.Time
	size    int64
}

// ProjectInfo is the repository-level metadata shown by "fossil info" and
// "fossil json stat".
type ProjectInfo struct {
	Name           string `json:"projectName"`
	Description    string `json:"projectDescription"`
	ProjectCode    string `json:"projectCode"`
	RepositorySize int64  `json:"repositorySize"`
	HashPolicy     int    `json:"hashPolicy"`
	CheckinCount   int    `json:"checkinCount"`
	RepositoryPath string `json:"repositoryPath"`
	DefaultUser    string `json:"defaultUser"`
	RawConfig      map[string]string
}

type statPayload struct {
	ProjectName        string `json:"projectName"`
	ProjectDescription string `json:"projectDescription"`
	ProjectCode        string `json:"projectCode"`
	RepositorySize     int64  `json:"repositorySize"`
}

// IsValidFossilRepository reports whether p looks like a Fossil repository
// file or a Fossil check-out directory. The check is cheap: it recognizes the
// SQLite file header and the .fslckout/_FOSSIL_ marker, and does not execute
// fossil.
func IsValidFossilRepository(p string) bool {
	repo, _, ok := locateFossilPath(p)
	if !ok {
		return false
	}
	if repo == "" {
		return true
	}
	return looksLikeSQLite(repo)
}

func looksLikeSQLite(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	var header [16]byte
	if _, err := f.Read(header[:]); err != nil {
		return false
	}
	return string(header[:]) == "SQLite format 3\x00"
}

// locateFossilPath returns the repository path and check-out path implied by
// p. For a check-out directory the repository path is empty and must be
// discovered with fossil info.
func locateFossilPath(p string) (repoPath, checkoutPath string, ok bool) {
	if p == "" {
		return "", "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", "", false
	}
	if st.IsDir() {
		if fileExists(filepath.Join(abs, ".fslckout")) || fileExists(filepath.Join(abs, "_FOSSIL_")) {
			return "", abs, true
		}
		return "", "", false
	}
	switch filepath.Base(abs) {
	case ".fslckout", "_FOSSIL_":
		return "", filepath.Dir(abs), true
	}
	return abs, "", true
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// NewLocalFossilRepository opens a repository file or a check-out directory.
// Opening a repository file does not spawn fossil; the first operation that
// needs data does. Opening a check-out directory runs "fossil info" once to
// discover the repository file.
func NewLocalFossilRepository(p string) (*LocalFossilRepository, error) {
	repoPath, checkoutPath, ok := locateFossilPath(p)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFossilRepository, p)
	}
	r := &LocalFossilRepository{
		RepositoryPath: repoPath,
		CheckoutPath:   checkoutPath,
		commits:        make(map[string]*CommitObject),
		manifests:      make(map[string]*ManifestObject),
		infoByName:     make(map[string]*commitInfo),
		prefixHashes:   make(map[string]string),
		lastCommit:     make(map[string]map[string]string),
	}
	if checkoutPath != "" {
		info, err := r.readCheckoutInfo()
		if err != nil {
			return nil, err
		}
		r.RepositoryPath = info.repoPath
		r.Description = info.project.Description
	}
	if r.RepositoryPath == "" {
		return nil, fmt.Errorf("%w: %s", ErrNotFossilRepository, p)
	}
	if !looksLikeSQLite(r.RepositoryPath) {
		return nil, fmt.Errorf("%w: %s", ErrNotFossilRepository, r.RepositoryPath)
	}
	if abs, err := filepath.Abs(r.RepositoryPath); err == nil {
		r.RepositoryPath = abs
	}
	return r, nil
}

// Initialize a repository file at p with "fossil init". The parent directory
// is created if needed.
func InitLocalFossilRepository(p string) (*LocalFossilRepository, error) {
	return InitLocalFossilRepositoryWithOptions(p, InitOptions{})
}

// InitOptions controls InitLocalFossilRepositoryWithOptions.
type InitOptions struct {
	ProjectName        string
	ProjectDescription string
	AdminUser          string
	Template           string
	DateOverride       string
	SHA1               bool
}

// InitLocalFossilRepositoryWithOptions creates a new Fossil repository.
func InitLocalFossilRepositoryWithOptions(p string, opts InitOptions) (*LocalFossilRepository, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	args := []string{"init", abs}
	if opts.ProjectName != "" {
		args = append(args, "--project-name", opts.ProjectName)
	}
	if opts.ProjectDescription != "" {
		args = append(args, "--project-desc", opts.ProjectDescription)
	}
	if opts.AdminUser != "" {
		args = append(args, "--admin-user", opts.AdminUser)
	}
	if opts.Template != "" {
		args = append(args, "--template", opts.Template)
	}
	if opts.DateOverride != "" {
		args = append(args, "--date-override", opts.DateOverride)
	}
	if opts.SHA1 {
		args = append(args, "--sha1")
	}
	if _, err := runFossilIn(filepath.Dir(abs), args...); err != nil {
		return nil, err
	}
	return NewLocalFossilRepository(abs)
}

type checkoutInfo struct {
	repoPath string
	project  *ProjectInfo
}

func (r *LocalFossilRepository) readCheckoutInfo() (*checkoutInfo, error) {
	out, err := runInCheckout(r.CheckoutPath, "info")
	if err != nil {
		return nil, err
	}
	info := &checkoutInfo{project: &ProjectInfo{}}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "repository":
			info.repoPath = value
		case "project-name":
			info.project.Name = value
		case "project-code":
			info.project.ProjectCode = value
		case "check-ins":
			info.project.CheckinCount = parseInt(value)
		case "checkout":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				info.project.RawConfig = map[string]string{"checkout": fields[0]}
			}
		}
	}
	if info.repoPath == "" {
		return nil, fmt.Errorf("%w: %s is not a fossil check-out", ErrNotFossilRepository, r.CheckoutPath)
	}
	if !filepath.IsAbs(info.repoPath) {
		info.repoPath = filepath.Join(r.CheckoutPath, info.repoPath)
	}
	return info, nil
}

// Validate runs a real repository query. It is useful after constructing a
// handle from a file whose SQLite header looked right but which is not a
// Fossil repository.
func (r *LocalFossilRepository) Validate() error {
	_, err := r.GetProjectInfo()
	return err
}

// Close releases the handle. It is a no-op because the package keeps no open
// file descriptors; the method exists for symmetry with other SCM libraries.
func (r *LocalFossilRepository) Close() error { return nil }

// Refresh drops all cached data. Call it after another process pushes into the
// repository if the same handle remains open.
func (r *LocalFossilRepository) Refresh() error {
	r.mu.Lock()
	r.stamp = repoStamp{}
	r.hashPolicy = 0
	r.resolvedCommitUser = ""
	r.commits = make(map[string]*CommitObject)
	r.manifests = make(map[string]*ManifestObject)
	r.infoByName = make(map[string]*commitInfo)
	r.prefixHashes = make(map[string]string)
	r.branches = nil
	r.tags = nil
	r.project = nil
	r.lastCommit = make(map[string]map[string]string)
	r.mu.Unlock()
	return nil
}

func (r *LocalFossilRepository) currentStamp() repoStamp {
	st, err := os.Stat(r.RepositoryPath)
	if err != nil {
		return repoStamp{}
	}
	return repoStamp{modTime: st.ModTime(), size: st.Size()}
}

// ensureStamp clears caches whose contents can change when the repository is
// modified: branch heads, the tag set and project metadata. Artifacts are
// immutable, so parsed commits and manifests are kept.
func (r *LocalFossilRepository) ensureStamp() {
	st := r.currentStamp()
	r.mu.Lock()
	if r.stamp != st {
		r.stamp = st
		r.branches = nil
		r.tags = nil
		r.project = nil
		r.clearNameCacheLocked()
	}
	r.mu.Unlock()
}

func (r *LocalFossilRepository) clearNameCacheLocked() {
	r.infoByName = make(map[string]*commitInfo)
}

// invalidateNames clears the caches whose keys are mutable names: branch
// heads, the tag set and the name-to-hash map. Parsed artifacts are immutable
// and are kept.
func (r *LocalFossilRepository) invalidateNames() {
	r.mu.Lock()
	r.branches = nil
	r.tags = nil
	r.clearNameCacheLocked()
	r.mu.Unlock()
}

func (r *LocalFossilRepository) invalidateAfterWrite() {
	r.mu.Lock()
	r.stamp = repoStamp{}
	r.branches = nil
	r.tags = nil
	r.project = nil
	r.clearNameCacheLocked()
	r.mu.Unlock()
}

// GetProjectInfo returns repository-level metadata. It is cached until the
// repository file changes or Refresh is called.
func (r *LocalFossilRepository) GetProjectInfo() (*ProjectInfo, error) {
	r.ensureStamp()
	r.mu.RLock()
	cached := r.project
	r.mu.RUnlock()
	if cached != nil && cached.Name != "" {
		cp := *cached
		return &cp, nil
	}

	payload, err := r.runJSON("stat")
	if err != nil {
		return nil, err
	}
	var stat statPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &stat); err != nil {
			return nil, fmt.Errorf("%w: fossil json stat: %v", ErrInvalidFormat, err)
		}
	}
	res := &ProjectInfo{
		Name:           stat.ProjectName,
		Description:    stat.ProjectDescription,
		ProjectCode:    stat.ProjectCode,
		RepositorySize: stat.RepositorySize,
		RepositoryPath: r.RepositoryPath,
	}

	if cfgPayload, cfgErr := r.runJSON("config", "get", "project"); cfgErr == nil {
		var cfg map[string]any
		if json.Unmarshal(cfgPayload, &cfg) == nil {
			res.RawConfig = make(map[string]string, len(cfg))
			for k, v := range cfg {
				res.RawConfig[k] = fmt.Sprint(v)
			}
			if res.Name == "" {
				res.Name = res.RawConfig["project-name"]
			}
			if res.Description == "" {
				res.Description = res.RawConfig["project-description"]
			}
			switch res.RawConfig["hash-policy"] {
			case "1", "sha1":
				res.HashPolicy = 1
			case "2", "sha3", "sha3-256":
				res.HashPolicy = 2
			}
		}
	}
	if infoOut, infoErr := r.run("info"); infoErr == nil {
		for _, line := range strings.Split(string(infoOut), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			if strings.TrimSpace(key) == "check-ins" {
				res.CheckinCount = parseInt(value)
			}
		}
	}

	r.mu.Lock()
	r.project = res
	r.Description = res.Description
	if res.HashPolicy != 0 {
		r.hashPolicy = res.HashPolicy
	}
	r.mu.Unlock()
	cp := *res
	return &cp, nil
}

// GetHashPolicy returns 1 for SHA1 repositories and 2 for SHA3-256.
func (r *LocalFossilRepository) GetHashPolicy() (int, error) {
	r.mu.RLock()
	cached := r.hashPolicy
	r.mu.RUnlock()
	if cached != 0 {
		return cached, nil
	}
	info, err := r.GetProjectInfo()
	if err != nil {
		return 0, err
	}
	if info.HashPolicy != 0 {
		r.mu.Lock()
		r.hashPolicy = info.HashPolicy
		r.mu.Unlock()
	}
	return info.HashPolicy, nil
}

// resolveBranchHead resolves one branch name. Unlike GetAllBranchList it only
// spawns one fossil process, which matters on the commit path.
func (r *LocalFossilRepository) resolveBranchHead(name string) (string, bool, error) {
	info, err := r.getInfo(name)
	if err != nil {
		if isNotFoundError(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.Type != "" && info.Type != "checkin" {
		return "", false, nil
	}
	return info.Hash, true, nil
}

// IsEmpty reports whether the repository contains no files at its tip. A
// freshly initialized Fossil repository always has one empty check-in, so
// "no check-ins at all" is not a state Fossil produces.
func (r *LocalFossilRepository) IsEmpty() bool {
	tip, ok, err := r.resolveBranchHead("trunk")
	if err != nil || !ok {
		tip, err = r.getTipHash()
	}
	if err != nil {
		return true
	}
	m, err := r.loadManifest(tip)
	if err != nil {
		return false
	}
	return len(m.ItemList) == 0
}

func (r *LocalFossilRepository) getTipHash() (string, error) {
	payload, err := r.runJSON("timeline", "checkin", "-n", "1")
	if err != nil {
		return "", err
	}
	var tl timelinePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &tl); err != nil {
			return "", fmt.Errorf("%w: fossil json timeline: %v", ErrInvalidFormat, err)
		}
	}
	if len(tl.Timeline) == 0 {
		return "", ErrEmptyRepository
	}
	return normalizeHash(tl.Timeline[0].UUID), nil
}

// GetTip returns the most recent check-in in the repository. It is not
// necessarily the head of the trunk branch; use GetBranchTip for that.
func (r *LocalFossilRepository) GetTip() (*CommitObject, error) {
	id, err := r.getTipHash()
	if err != nil {
		return nil, err
	}
	return r.loadCommit(id, true)
}

// GetBranchTip returns the newest leaf of a branch.
func (r *LocalFossilRepository) GetBranchTip(branch string) (*CommitObject, error) {
	hash, ok, err := r.resolveBranchHead(branch)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrObjectNotFound
	}
	return r.loadCommit(hash, true)
}

// GetCommit returns a check-in by full hash or unique prefix.
func (r *LocalFossilRepository) GetCommit(id string) (*CommitObject, error) {
	full, err := r.resolveHash(id)
	if err != nil {
		return nil, err
	}
	return r.loadCommit(full, true)
}

// GetCommitByNode is an alias for GetCommit kept for parity with the
// Mercurial and Git libraries.
func (r *LocalFossilRepository) GetCommitByNode(id string) (*CommitObject, error) {
	return r.GetCommit(id)
}

// loadCommit fetches and parses a check-in artifact, optionally computing the
// list of files changed relative to the primary parent.
func (r *LocalFossilRepository) loadCommit(id string, withChanges bool) (*CommitObject, error) {
	id = normalizeHash(id)
	r.mu.RLock()
	cached := r.commits[id]
	r.mu.RUnlock()
	if cached != nil {
		if !withChanges || atomic.LoadUint32(&cached.changesFilled) != 0 {
			return cached, nil
		}
		if cached.rawData != nil {
			// The artifact is already here; only the changed-file list is
			// missing. Avoid asking fossil for the same artifact again.
			r.fillMu.Lock()
			defer r.fillMu.Unlock()
			if atomic.LoadUint32(&cached.changesFilled) == 0 {
				if err := r.fillChanges(cached); err != nil {
					return nil, err
				}
				atomic.StoreUint32(&cached.changesFilled, 1)
			}
			return cached, nil
		}
	}
	raw, err := r.run("artifact", id)
	if err != nil {
		return nil, err
	}
	if full := artifactFullHash(raw, id); full != "" {
		id = full
	}
	art, err := ParseArtifact(id, raw)
	if err != nil {
		return nil, err
	}
	if art.Kind != ArtifactCheckin {
		return nil, fmt.Errorf("%w: %s is a %s artifact, not a check-in", ErrObjectNotFound, id, art.Kind)
	}
	c := commitFromArtifact(art)
	// Publish the full artifact before computing changes: fillChanges parses
	// this check-in's manifest, and loadManifest can reuse rawData instead of
	// asking fossil for the same artifact a second time.
	r.mu.Lock()
	if old := r.commits[id]; old == nil || old.rawData == nil || c.rawData != nil {
		r.commits[id] = c
	}
	r.mu.Unlock()
	if withChanges {
		r.fillMu.Lock()
		defer r.fillMu.Unlock()
		if atomic.LoadUint32(&c.changesFilled) == 0 {
			if err := r.fillChanges(c); err != nil {
				return nil, err
			}
			atomic.StoreUint32(&c.changesFilled, 1)
		}
	}
	return c, nil
}

// resolveHash turns a unique prefix, tag or branch name into a full check-in
// hash. Fossil itself performs the resolution, so ambiguity and tag
// propagation follow Fossil's rules.
func (r *LocalFossilRepository) resolveHash(id string) (string, error) {
	id = normalizeHash(id)
	if id == "" {
		return "", ErrObjectNotFound
	}
	if IsValidHash(id) && hexHashRE.MatchString(id) {
		// A 40-character name may be a complete SHA1 hash or a SHA3-256
		// prefix. loadCommit/loadManifest canonicalize it from the fetched
		// artifact bytes, so no extra repository lookup is needed here.
		return id, nil
	}
	if !IsValidHashPrefix(id) && !validBranchName(id) && strings.ContainsAny(id, " \t\r\n<>") {
		return "", fmt.Errorf("%w: invalid artifact name %q", ErrObjectNotFound, id)
	}
	r.mu.RLock()
	for full := range r.commits {
		if strings.HasPrefix(full, id) {
			r.mu.RUnlock()
			return full, nil
		}
	}
	r.mu.RUnlock()

	r.mu.Lock()
	if full, ok := r.prefixHashes[id]; ok {
		r.mu.Unlock()
		return full, nil
	}
	r.mu.Unlock()

	info, err := r.getInfo(id)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrObjectNotFound, id, err)
	}
	return info.Hash, nil
}

// listTimelineHashes returns full check-in hashes from a single timeline
// page, newest first.
func (r *LocalFossilRepository) listTimelineHashes(limit int) ([]string, error) {
	args := []string{"timeline", "checkin", "-n", strconv.Itoa(limit)}
	payload, err := r.runJSON(args...)
	if err != nil {
		return nil, err
	}
	var tl timelinePayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &tl); err != nil {
			return nil, err
		}
	}
	res := make([]string, 0, len(tl.Timeline))
	for _, e := range tl.Timeline {
		if e.UUID != "" {
			res = append(res, normalizeHash(e.UUID))
		}
	}
	return res, nil
}

// resolveHashPrefixes expands many abbreviated artifact names at once. One
// timeline page resolves the common case; only prefixes older than the page
// fall back to one "fossil json artifact" call each.
func (r *LocalFossilRepository) resolveHashPrefixes(prefixes []string) map[string]string {
	res := make(map[string]string, len(prefixes))
	pending := make(map[string]bool)
	policy := 0
	policyLoaded := false
	for _, raw := range prefixes {
		p := normalizeHash(raw)
		if p == "" || !IsValidHashPrefix(p) {
			continue
		}
		if len(p) == 64 {
			res[p] = p
			continue
		}
		if len(p) == 40 {
			if !policyLoaded {
				policy, _ = r.GetHashPolicy()
				policyLoaded = true
			}
			if policy != 2 {
				res[p] = p
				continue
			}
		}
		r.mu.RLock()
		full, ok := r.prefixHashes[p]
		if !ok {
			for candidate := range r.commits {
				if strings.HasPrefix(candidate, p) {
					full, ok = candidate, true
					break
				}
			}
		}
		r.mu.RUnlock()
		if ok {
			res[p] = full
			continue
		}
		pending[p] = true
	}
	if len(pending) == 0 {
		return res
	}
	if hashes, err := r.listTimelineHashes(1000); err == nil {
		for _, full := range hashes {
			for p := range pending {
				if strings.HasPrefix(full, p) {
					res[p] = full
					delete(pending, p)
				}
			}
			if len(pending) == 0 {
				break
			}
		}
	}
	if len(pending) > 0 {
		list := make([]string, 0, len(pending))
		for p := range pending {
			list = append(list, p)
		}
		full, _ := parallelMap(list, 4, func(p string) (string, error) {
			info, err := r.getInfo(p)
			if err != nil {
				return "", err
			}
			return info.Hash, nil
		})
		for i, p := range list {
			if i < len(full) && full[i] != "" {
				res[p] = full[i]
			}
		}
	}
	r.mu.Lock()
	for p, full := range res {
		if p != full {
			r.prefixHashes[p] = full
		}
	}
	r.mu.Unlock()
	return res
}

// commitInfo is the subset of "fossil info" / "fossil json artifact" that the
// library consumes.
type commitInfo struct {
	Hash    string
	Type    string
	Parents []string
	Tags    []string
	Comment string
	Date    time.Time
	User    string
}

type artifactInfoPayload struct {
	Type      string   `json:"type"`
	UUID      string   `json:"uuid"`
	Timestamp int64    `json:"timestamp"`
	User      string   `json:"user"`
	Comment   string   `json:"comment"`
	Parents   []string `json:"parents"`
	Tags      []string `json:"tags"`
}

var (
	reInfoDate = regexp.MustCompile(`(?m)^\s*hash:\s+([0-9a-fA-F]+)(?:\s+(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}))?`)
	reParent   = regexp.MustCompile(`(?m)^parent:\s+([0-9a-fA-F]+)`)
	reTagLine  = regexp.MustCompile(`(?m)^tags:\s+(.*)$`)
	reComment  = regexp.MustCompile(`(?m)^comment:\s+(.*)$`)
)

func (r *LocalFossilRepository) getInfo(name string) (*commitInfo, error) {
	key := normalizeHash(name)
	r.mu.RLock()
	if ci, ok := r.infoByName[key]; ok {
		r.mu.RUnlock()
		return ci, nil
	}
	r.mu.RUnlock()

	// "fossil info" truncates SHA3-256 hashes to 40 characters, so ask the
	// JSON artifact endpoint instead. It always returns the full UUID and
	// resolves branches and tags by name.
	ci, err := r.getInfoJSON(name)
	if err != nil {
		// Fall back to fossil info for very old Fossil versions.
		out, infoErr := r.run("info", name)
		if infoErr != nil {
			return nil, err
		}
		ci = parseFossilInfo(string(out))
		if ci.Hash == "" {
			return nil, fmt.Errorf("%w: cannot resolve %q", ErrObjectNotFound, name)
		}
	}
	r.mu.Lock()
	r.infoByName[key] = ci
	r.infoByName[ci.Hash] = ci
	r.mu.Unlock()
	return ci, nil
}

func (r *LocalFossilRepository) getInfoJSON(name string) (*commitInfo, error) {
	payload, err := r.runJSON("artifact", "--name", name)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrObjectNotFound, name)
	}
	var pl artifactInfoPayload
	if err := json.Unmarshal(payload, &pl); err != nil {
		return nil, fmt.Errorf("%w: fossil json artifact: %v", ErrInvalidFormat, err)
	}
	if pl.UUID == "" {
		return nil, fmt.Errorf("%w: cannot resolve %q", ErrObjectNotFound, name)
	}
	ci := &commitInfo{
		Hash:    normalizeHash(pl.UUID),
		Type:    pl.Type,
		Parents: make([]string, len(pl.Parents)),
		Tags:    append([]string(nil), pl.Tags...),
		Comment: pl.Comment,
		User:    pl.User,
	}
	for i := range pl.Parents {
		ci.Parents[i] = normalizeHash(pl.Parents[i])
	}
	if pl.Timestamp > 0 {
		ci.Date = time.Unix(pl.Timestamp, 0).UTC()
	}
	return ci, nil
}

func parseFossilInfo(text string) *commitInfo {
	ci := &commitInfo{}
	if m := reInfoDate.FindStringSubmatch(text); len(m) > 0 {
		ci.Hash = normalizeHash(m[1])
		if len(m) > 2 && m[2] != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[2], time.UTC); err == nil {
				ci.Date = t
			}
		}
	}
	for _, m := range reParent.FindAllStringSubmatch(text, -1) {
		ci.Parents = append(ci.Parents, normalizeHash(m[1]))
	}
	if m := reTagLine.FindStringSubmatch(text); len(m) > 1 {
		for _, tag := range strings.Split(m[1], ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				ci.Tags = append(ci.Tags, tag)
			}
		}
	}
	if m := reComment.FindStringSubmatch(text); len(m) > 1 {
		ci.Comment = strings.TrimSpace(m[1])
	}
	return ci
}

// SetCommitUser pins the Fossil login used for writes. Most deployments can
// leave this empty: when Fossil cannot determine the current user from the
// environment, the library picks a repository login that has check-in
// capability.
func (r *LocalFossilRepository) SetCommitUser(login string) {
	r.mu.Lock()
	r.CommitUser = login
	r.resolvedCommitUser = ""
	r.mu.Unlock()
}

func (r *LocalFossilRepository) chooseCommitUser() string {
	r.mu.RLock()
	if r.CommitUser != "" {
		u := r.CommitUser
		r.mu.RUnlock()
		return u
	}
	if r.resolvedCommitUser != "" {
		u := r.resolvedCommitUser
		r.mu.RUnlock()
		return u
	}
	r.mu.RUnlock()

	out, err := r.run("user", "list")
	if err != nil {
		return ""
	}
	logins := make([]string, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		logins = append(logins, fields[0])
	}
	// Prefer developer: it is a standard Fossil user with check-in
	// capability. Fall back to any user that can check in.
	ordered := make([]string, 0, len(logins))
	for _, login := range logins {
		if login == "developer" {
			ordered = append(ordered, login)
		}
	}
	for _, login := range logins {
		if login != "developer" {
			ordered = append(ordered, login)
		}
	}
	chosen := ""
	for _, login := range ordered {
		caps, err := r.run("user", "capabilities", login)
		if err != nil {
			continue
		}
		c := strings.TrimSpace(string(caps))
		if strings.Contains(c, "i") {
			chosen = login
			break
		}
		if chosen == "" && strings.Contains(c, "s") {
			chosen = login
		}
	}
	if chosen == "" && len(logins) > 0 {
		chosen = logins[len(logins)-1]
	}
	r.mu.Lock()
	r.resolvedCommitUser = chosen
	r.mu.Unlock()
	return chosen
}

// runAsUser runs a write command, retrying with an explicit login when
// Fossil cannot determine the current user from the environment.
func (r *LocalFossilRepository) runAsUser(args ...string) ([]byte, error) {
	r.mu.RLock()
	login := r.CommitUser
	r.mu.RUnlock()
	if login != "" {
		args = append(args, "--user", login)
	}
	out, err := r.run(args...)
	if err == nil || !isUserResolutionError(err) {
		return out, err
	}
	if login == "" {
		login = r.chooseCommitUser()
		if login == "" {
			return out, err
		}
		retry := append(append([]string(nil), args...), "--user", login)
		return r.run(retry...)
	}
	return out, err
}

func isUserResolutionError(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	text := strings.ToLower(ce.Stdout + "\n" + ce.Stderr)
	for _, needle := range []string{
		"cannot determine user",
		"no such user",
		"not authorized",
		"permission denied",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// GetWorkingDirBranch returns the branch of the open check-out, or an error
// when the handle was opened from a repository file rather than a check-out.
func (r *LocalFossilRepository) GetWorkingDirBranch() (string, error) {
	if r.CheckoutPath == "" {
		return "", errors.New("repository handle has no check-out")
	}
	out, err := runInCheckout(r.CheckoutPath, "branch", "current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetWorkingDirParents returns the check-out's current version and primary
// parent. Both are empty when the check-out is not on a version.
func (r *LocalFossilRepository) GetWorkingDirParents() (string, string, error) {
	if r.CheckoutPath == "" {
		return "", "", errors.New("repository handle has no check-out")
	}
	out, err := runInCheckout(r.CheckoutPath, "info")
	if err != nil {
		return "", "", err
	}
	var version, parent string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		switch strings.TrimSpace(key) {
		case "checkout":
			version = normalizeHash(fields[0])
		case "parent":
			parent = normalizeHash(fields[0])
		}
	}
	if version != "" {
		if c, err := r.loadCommit(version, false); err == nil {
			version = c.Id
		}
	}
	if parent != "" {
		if c, err := r.loadCommit(parent, false); err == nil {
			parent = c.Id
		}
	}
	return version, parent, nil
}

// RepositoryFileExists is a convenience for callers that accept a path from
// configuration and want to distinguish "missing" from "not a repository".
func RepositoryFileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
