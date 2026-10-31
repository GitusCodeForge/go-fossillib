package fossillib

import "errors"

var (
	// ErrNotFossilRepository is returned when the path does not look like a
	// Fossil repository.
	ErrNotFossilRepository = errors.New("not a fossil repository")
	// ErrEmptyRepository is returned when an operation needs a check-in but
	// the repository has none.
	ErrEmptyRepository = errors.New("the fossil repository has no check-ins")
	// ErrObjectNotFound is returned when an artifact or path does not exist.
	ErrObjectNotFound = errors.New("object not found")
	// ErrInvalidFormat is returned when an artifact or diff cannot be parsed.
	ErrInvalidFormat = errors.New("invalid fossil artifact format")
	// ErrMergeConflict is returned when a merge leaves conflicts.
	ErrMergeConflict = errors.New("merge has conflicts")
)

// FossilObjectType is the kind of a Fossil artifact.
//
// Fossil does not have a typed object store in the Git sense. The hash of an
// artifact is just the hash of its bytes; the same hash can name a file
// version, a check-in, a wiki page or a tag control artifact. The type is a
// property of the content, not of the name.
type FossilObjectType int

const (
	INVALID FossilObjectType = iota
	// CHECKIN is a check-in manifest.
	CHECKIN
	// MANIFEST is a complete file listing. In Fossil the check-in manifest
	// is also the file listing, so ManifestObject and CommitObject both
	// report MANIFEST/CHECKIN respectively.
	MANIFEST
	// FILERev is a content artifact, one version of one file.
	FILERev
	// CONTROL is a tag control artifact.
	CONTROL
)

func (t FossilObjectType) String() string {
	switch t {
	case CHECKIN:
		return "CHECKIN"
	case MANIFEST:
		return "MANIFEST"
	case FILERev:
		return "FILERev"
	case CONTROL:
		return "CONTROL"
	default:
		return "INVALID"
	}
}

// FossilObject is implemented by check-ins, manifests and file revisions.
type FossilObject interface {
	Type() FossilObjectType
	ObjectId() string
	RawData() []byte
}

// IsCommitObject reports whether o is a check-in.
func IsCommitObject(o FossilObject) bool {
	_, ok := o.(*CommitObject)
	return ok
}

// IsManifestObject reports whether o is a manifest view.
func IsManifestObject(o FossilObject) bool {
	_, ok := o.(*ManifestObject)
	return ok
}

// IsBlobObject reports whether o is a file content artifact.
func IsBlobObject(o FossilObject) bool {
	_, ok := o.(*BlobObject)
	return ok
}

// IsTreeObject reports whether o is a directory view.
func IsTreeObject(o FossilObject) bool {
	_, ok := o.(*TreeObject)
	return ok
}
