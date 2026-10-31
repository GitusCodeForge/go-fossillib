package fossillib

import (
	"fmt"
	"strings"
)

// Fossil stores only one permission bit per file: the executable bit. A
// symbolic link, when the repository enables symlinks, is a normal file whose
// content is the link target. These constants expose Fossil's vocabulary as
// Git-like modes so callers do not have to special-case it.
const (
	FOSSIL_MODE_NORMAL_FILE     = 0644
	FOSSIL_MODE_EXECUTABLE_FILE = 0755
	FOSSIL_MODE_SYMBOLIC_LINK   = 0120000
	FOSSIL_MODE_TREE            = 0040000
)

// BlobObject is one content artifact: one version of one file. Data is the
// raw file content, without any Fossil framing.
type BlobObject struct {
	Id    string
	Data  []byte
	Path  string
	Flags string
	Mode  int
}

func (b *BlobObject) Type() FossilObjectType { return FILERev }
func (b *BlobObject) ObjectId() string       { return b.Id }
func (b *BlobObject) RawData() []byte        { return b.Data }

func (b *BlobObject) String() string {
	return fmt.Sprintf("Blob{%s,%d}", b.Id, len(b.Data))
}

// TreeObjectItem is one entry of a directory listing.
type TreeObjectItem struct {
	Mode  int
	Name  string
	Hash  string
	IsDir bool
}

func (t TreeObjectItem) String() string {
	return fmt.Sprintf("<<[%o]%s,%s>>", t.Mode, t.Name, t.Hash)
}

// TreeObject is a directory view built from a check-in manifest. Fossil does
// not store directory objects; directories are implied by the slashes in the
// flat file list.
type TreeObject struct {
	Id         string
	Path       string
	ObjectList []TreeObjectItem

	manifest *ManifestObject
	commit   *CommitObject
	repo     *LocalFossilRepository
}

func (t *TreeObject) Type() FossilObjectType { return MANIFEST }
func (t *TreeObject) ObjectId() string       { return t.Id }
func (t *TreeObject) RawData() []byte {
	if t.manifest == nil {
		return nil
	}
	return t.manifest.RawData()
}

func (t *TreeObject) String() string {
	return fmt.Sprintf("Tree{%s,%s,%s}", t.Id, t.Path, t.ObjectList)
}

// GetManifest returns the complete manifest of a check-in.
func (r *LocalFossilRepository) GetManifest(c *CommitObject) (*ManifestObject, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	return r.loadManifest(c.Id)
}

// GetManifestByNode returns a manifest by check-in hash or prefix.
func (r *LocalFossilRepository) GetManifestByNode(node string) (*ManifestObject, error) {
	full, err := r.resolveHash(node)
	if err != nil {
		return nil, err
	}
	return r.loadManifest(full)
}

func (r *LocalFossilRepository) loadManifest(id string) (*ManifestObject, error) {
	return r.loadManifestDepth(id, 0)
}

func (r *LocalFossilRepository) loadManifestDepth(id string, depth int) (*ManifestObject, error) {
	id = normalizeHash(id)
	if depth > 64 {
		return nil, fmt.Errorf("%w: delta manifest chain is too deep at %s", ErrInvalidFormat, id)
	}
	r.mu.RLock()
	if m := r.manifests[id]; m != nil {
		r.mu.RUnlock()
		return m, nil
	}
	r.mu.RUnlock()

	var raw []byte
	r.mu.RLock()
	if c := r.commits[id]; c != nil && c.rawData != nil {
		raw = c.rawData
	}
	r.mu.RUnlock()
	if len(raw) == 0 {
		var err error
		raw, err = r.run("artifact", id)
		if err != nil {
			return nil, err
		}
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
	var m *ManifestObject
	if art.Baseline != "" {
		base, err := r.loadManifestDepth(art.Baseline, depth+1)
		if err != nil {
			return nil, err
		}
		m = applyDelta(base, art)
	} else {
		m = fullManifestFromArtifact(art)
	}
	r.mu.Lock()
	r.manifests[id] = m
	r.mu.Unlock()
	return m, nil
}

// GetTree returns a directory listing of dir in the check-in's manifest. dir
// is repository-root-relative; "" means the root.
func (r *LocalFossilRepository) GetTree(c *CommitObject, dir string) (*TreeObject, error) {
	m, err := r.GetManifest(c)
	if err != nil {
		return nil, err
	}
	dir = normalizeManifestPath(dir)
	if dir != "" && !m.HasDir(dir) {
		return nil, ErrObjectNotFound
	}
	return &TreeObject{
		Id:         m.Id,
		Path:       dir,
		ObjectList: m.Children(dir),
		manifest:   m,
		commit:     c,
		repo:       r,
	}, nil
}

// GetTreeAt is GetTree for a check-in hash or prefix.
func (r *LocalFossilRepository) GetTreeAt(id string, dir string) (*TreeObject, error) {
	full, err := r.resolveHash(id)
	if err != nil {
		return nil, err
	}
	// A tree page does not need the changed-file list, only the manifest.
	c, err := r.loadCommit(full, false)
	if err != nil {
		return nil, err
	}
	return r.GetTree(c, dir)
}

// ResolveTreePath walks p from the directory view t and returns either a
// *TreeObject for a directory or a *BlobObject for a file.
func (r *LocalFossilRepository) ResolveTreePath(t *TreeObject, p string) (FossilObject, error) {
	if t == nil || t.manifest == nil {
		return nil, fmt.Errorf("tree view has no manifest")
	}
	cur := t.Path
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return t, nil
	}
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return nil, ErrObjectNotFound
		}
		next := part
		if cur != "" {
			next = cur + "/" + part
		}
		if item, ok := t.manifest.Get(next); ok {
			if i != len(parts)-1 {
				return nil, ErrObjectNotFound
			}
			return r.readBlob(next, item.BlobId, item.Flags, item.Mode)
		}
		if t.manifest.HasDir(next) {
			cur = next
			continue
		}
		return nil, ErrObjectNotFound
	}
	return &TreeObject{
		Id:         t.manifest.Id,
		Path:       cur,
		ObjectList: t.manifest.Children(cur),
		manifest:   t.manifest,
		commit:     t.commit,
		repo:       t.repo,
	}, nil
}

func (r *LocalFossilRepository) readBlob(path, blobId, flags string, mode int) (*BlobObject, error) {
	blobId = normalizeHash(blobId)
	if !IsValidHash(blobId) {
		return nil, fmt.Errorf("%w: invalid blob hash %q", ErrObjectNotFound, blobId)
	}
	data, err := r.run("artifact", blobId)
	if err != nil {
		return nil, err
	}
	if full := artifactFullHash(data, blobId); full != "" {
		blobId = full
	}
	return &BlobObject{
		Id:    blobId,
		Data:  data,
		Path:  path,
		Flags: flags,
		Mode:  mode,
	}, nil
}

// ReadBlob returns a content artifact by hash.
func (r *LocalFossilRepository) ReadBlob(blobId string) (*BlobObject, error) {
	return r.readBlob("", blobId, "", 0)
}

// GetFile returns one file of a check-in.
func (r *LocalFossilRepository) GetFile(c *CommitObject, filePath string) (*BlobObject, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	m, err := r.loadManifest(c.Id)
	if err != nil {
		return nil, err
	}
	item, ok := m.Get(filePath)
	if !ok {
		return nil, ErrObjectNotFound
	}
	return r.readBlob(item.Path, item.BlobId, item.Flags, item.Mode)
}

// GetFileContent is GetFile without the metadata wrapper.
func (r *LocalFossilRepository) GetFileContent(c *CommitObject, filePath string) ([]byte, error) {
	b, err := r.GetFile(c, filePath)
	if err != nil {
		return nil, err
	}
	return b.Data, nil
}

// GetFileAt returns one file of a check-in identified by hash or prefix.
func (r *LocalFossilRepository) GetFileAt(id string, filePath string) (*BlobObject, error) {
	c, err := r.GetCommit(id)
	if err != nil {
		return nil, err
	}
	return r.GetFile(c, filePath)
}

// GetFileContentAt is GetFileAt without the metadata wrapper.
func (r *LocalFossilRepository) GetFileContentAt(id string, filePath string) ([]byte, error) {
	b, err := r.GetFileAt(id, filePath)
	if err != nil {
		return nil, err
	}
	return b.Data, nil
}

// GetAllTrackedFilesAt returns every path in a check-in's manifest.
func (r *LocalFossilRepository) GetAllTrackedFilesAt(c *CommitObject) ([]string, error) {
	m, err := r.loadManifest(c.Id)
	if err != nil {
		return nil, err
	}
	res := make([]string, len(m.ItemList))
	for i := range m.ItemList {
		res[i] = m.ItemList[i].Path
	}
	return res, nil
}

// GetAllTrackedFiles returns every path at the repository tip.
func (r *LocalFossilRepository) GetAllTrackedFiles() ([]string, error) {
	tip, err := r.getTipHash()
	if err != nil {
		return nil, err
	}
	m, err := r.loadManifest(tip)
	if err != nil {
		return nil, err
	}
	res := make([]string, len(m.ItemList))
	for i := range m.ItemList {
		res[i] = m.ItemList[i].Path
	}
	return res, nil
}
