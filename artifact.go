package fossillib

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ArtifactKind is the structural kind inferred from an artifact's cards.
type ArtifactKind int

const (
	ArtifactUnknown ArtifactKind = iota
	ArtifactCheckin
	ArtifactCluster
	ArtifactControl
	ArtifactWiki
	ArtifactTicket
	ArtifactAttachment
	ArtifactTechnote
	ArtifactForum
)

func (k ArtifactKind) String() string {
	switch k {
	case ArtifactCheckin:
		return "checkin"
	case ArtifactCluster:
		return "cluster"
	case ArtifactControl:
		return "control"
	case ArtifactWiki:
		return "wiki"
	case ArtifactTicket:
		return "ticket"
	case ArtifactAttachment:
		return "attachment"
	case ArtifactTechnote:
		return "technote"
	case ArtifactForum:
		return "forum"
	default:
		return "unknown"
	}
}

// FileCard is one F card of a check-in manifest.
//
// Fossil's manifest format is documented as F *filename* ?*hash*?
// ?*permissions*? ?*old-name*?. A missing hash in a delta manifest means the
// file was deleted relative to the baseline.
type FileCard struct {
	Path    string
	BlobId  string
	HasBlob bool
	Perm    string
	OldPath string
}

// TagCard is one T card of a manifest or control artifact.
type TagCard struct {
	Op       byte // '+', '-' or '*'
	Name     string
	Target   string
	Value    string
	HasValue bool
}

// Artifact is the parsed card structure of one Fossil artifact.
type Artifact struct {
	Id        string
	Kind      ArtifactKind
	Raw       []byte
	Signature string

	Comment   string
	Date      time.Time
	DateRaw   string
	Baseline  string
	Parents   []string
	Files     []FileCard
	Tags      []TagCard
	User      string
	Mimetype  string
	Text      []byte
	Cluster   []string
	TicketId  string
	TicketJ   []TagCard
	WikiTitle string
	Checksum  string
}

// artifactScanner walks artifact cards. Structural artifacts are line
// oriented except for W cards, whose argument gives the byte length of the
// text that follows.
type artifactScanner struct {
	data []byte
	pos  int
}

func (s *artifactScanner) next() ([]byte, bool) {
	if s.pos >= len(s.data) {
		return nil, false
	}
	start := s.pos
	for s.pos < len(s.data) && s.data[s.pos] != '\n' {
		s.pos++
	}
	line := s.data[start:s.pos]
	if s.pos < len(s.data) {
		s.pos++ // consume newline
	}
	return line, true
}

// unescapeArtifact decodes the \s, \n and \\ escapes Fossil uses in card
// arguments.
func unescapeArtifact(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// isPermissionString reports whether s is a Fossil permission argument.
func isPermissionString(s string) bool {
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'x', 'l', 'w':
		default:
			return false
		}
	}
	return true
}

// stripPGPClearsign removes a PGP clear-sign envelope. Fossil permits
// structural artifacts to be clear-signed; the signature block is returned
// so callers can expose it.
func stripPGPClearsign(data []byte) (body []byte, signature string) {
	const begin = "-----BEGIN PGP SIGNED MESSAGE-----"
	const sigBegin = "-----BEGIN PGP SIGNATURE-----"
	if !bytes.HasPrefix(data, []byte(begin)) {
		return data, ""
	}
	// The armored header ends at the first empty line.
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		body = data[i+2:]
	} else {
		body = data
	}
	if i := bytes.Index(body, []byte(sigBegin)); i >= 0 {
		signature = strings.TrimSpace(string(body[i:]))
		body = body[:i]
	}
	return body, signature
}

// ParseArtifact parses one artifact as returned by "fossil artifact".
func ParseArtifact(id string, data []byte) (*Artifact, error) {
	body, signature := stripPGPClearsign(data)
	a := &Artifact{
		Id:        normalizeHash(id),
		Raw:       data,
		Signature: signature,
	}
	var jCards []TagCard
	var hasC, hasD, hasF, hasP, hasU, hasZ, hasM, hasW, hasL, hasE, hasG, hasH, hasI, hasK, hasA bool
	var hasT bool
	s := &artifactScanner{data: body}
	for {
		line, ok := s.next()
		if !ok {
			break
		}
		if len(line) == 0 {
			continue
		}
		card := line[0]
		args := line[1:]
		if len(args) > 0 && args[0] == ' ' {
			args = args[1:]
		}
		switch card {
		case 'B':
			a.Baseline = string(args)
		case 'C':
			a.Comment = unescapeArtifact(string(args))
			hasC = true
		case 'D':
			a.DateRaw = string(args)
			a.Date = parseFossilDate(string(args))
			hasD = true
		case 'F':
			fc, err := parseFileCard(string(args))
			if err != nil {
				return nil, fmt.Errorf("%w: artifact %s: %v", ErrInvalidFormat, id, err)
			}
			a.Files = append(a.Files, fc)
			hasF = true
		case 'P':
			a.Parents = strings.Fields(string(args))
			hasP = true
		case 'U':
			a.User = unescapeArtifact(string(args))
			hasU = true
		case 'Z':
			a.Checksum = string(args)
			hasZ = true
		case 'M':
			a.Cluster = append(a.Cluster, strings.Fields(string(args))...)
			hasM = true
		case 'T':
			tc, err := parseTagCard(string(args))
			if err != nil {
				return nil, fmt.Errorf("%w: artifact %s: %v", ErrInvalidFormat, id, err)
			}
			a.Tags = append(a.Tags, tc)
			hasT = true
		case 'N':
			a.Mimetype = unescapeArtifact(string(args))
		case 'K':
			a.TicketId = string(args)
			hasK = true
		case 'J':
			tc, err := parseTagCard(string(args))
			if err == nil {
				jCards = append(jCards, tc)
			}
		case 'L':
			a.WikiTitle = unescapeArtifact(string(args))
			hasL = true
		case 'E':
			hasE = true
		case 'G':
			hasG = true
		case 'H':
			hasH = true
		case 'I':
			hasI = true
		case 'A':
			hasA = true
		case 'W':
			hasW = true
			n := parseInt(string(args))
			if n < 0 || s.pos+n > len(s.data) {
				a.Text = append([]byte(nil), s.data[s.pos:]...)
				s.pos = len(s.data)
				break
			}
			a.Text = append([]byte(nil), s.data[s.pos:s.pos+n]...)
			s.pos += n
			// The W text is followed by one newline that is not part of
			// the text.
			if s.pos < len(s.data) && s.data[s.pos] == '\n' {
				s.pos++
			}
		default:
			// Unknown cards are tolerated. Fossils adds card types over
			// time and a reader should not reject a newer artifact.
		}
	}
	a.TicketJ = jCards

	switch {
	case hasC && hasD && hasU && !hasW && !hasK && !hasM && !hasA:
		a.Kind = ArtifactCheckin
	case hasK:
		a.Kind = ArtifactTicket
	case hasW && hasE:
		a.Kind = ArtifactTechnote
	case hasW && (hasG || hasH || hasI):
		a.Kind = ArtifactForum
	case hasW && hasL:
		a.Kind = ArtifactWiki
	case hasA && hasD:
		a.Kind = ArtifactAttachment
	case hasM && hasZ:
		a.Kind = ArtifactCluster
	case hasT && hasD && hasU && !hasC && !hasF && !hasP && !hasW:
		a.Kind = ArtifactControl
	default:
		a.Kind = ArtifactUnknown
	}
	return a, nil
}

func parseFileCard(s string) (FileCard, error) {
	fields := strings.Split(s, " ")
	if len(fields) == 0 || fields[0] == "" {
		return FileCard{}, fmt.Errorf("empty F card")
	}
	fc := FileCard{Path: unescapeArtifact(fields[0])}
	if len(fields) >= 2 && fields[1] != "" && hexHashRE.MatchString(fields[1]) {
		fc.BlobId = normalizeHash(fields[1])
		fc.HasBlob = true
	}
	rest := fields[1:]
	if fc.HasBlob {
		rest = fields[2:]
	}
	switch len(rest) {
	case 0:
	case 1:
		if isPermissionString(rest[0]) {
			fc.Perm = rest[0]
		} else {
			fc.OldPath = unescapeArtifact(rest[0])
		}
	default:
		if isPermissionString(rest[0]) {
			fc.Perm = rest[0]
			fc.OldPath = unescapeArtifact(rest[1])
		} else {
			fc.OldPath = unescapeArtifact(rest[0])
			if isPermissionString(rest[1]) {
				fc.Perm = rest[1]
			}
		}
	}
	return fc, nil
}

func parseTagCard(s string) (TagCard, error) {
	fields := strings.SplitN(s, " ", 3)
	if len(fields) < 2 || fields[0] == "" {
		return TagCard{}, fmt.Errorf("malformed T card")
	}
	name := fields[0]
	tc := TagCard{Op: name[0], Name: unescapeArtifact(name[1:])}
	if len(fields) >= 2 {
		tc.Target = fields[1]
	}
	if len(fields) >= 3 {
		tc.Value = unescapeArtifact(fields[2])
		tc.HasValue = true
	}
	return tc, nil
}

// parseFossilDate parses a D card timestamp. Fossil writes UTC in ISO-8601.
func parseFossilDate(s string) time.Time {
	layouts := []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ManifestItem is one file in a complete manifest.
type ManifestItem struct {
	Path    string
	BlobId  string
	Flags   string
	Mode    int
	OldPath string
}

func (m ManifestItem) String() string {
	return fmt.Sprintf("<<[%o]%s,%s>>", m.Mode, m.Path, m.BlobId)
}

// ManifestObject is the complete file list of one check-in. For a check-in
// whose artifact is a delta manifest, ItemList is resolved through the B card
// while RawData returns the original (possibly delta) artifact text.
type ManifestObject struct {
	Id       string
	CommitId string
	ItemList []ManifestItem
	Baseline string

	rawData []byte
	index   map[string]int
}

func (m *ManifestObject) Type() FossilObjectType { return MANIFEST }
func (m *ManifestObject) ObjectId() string       { return m.Id }
func (m *ManifestObject) RawData() []byte        { return m.rawData }

func (m *ManifestObject) String() string {
	return fmt.Sprintf("Manifest{%s,%d}", m.Id, len(m.ItemList))
}

func (m *ManifestObject) buildIndex() {
	m.index = make(map[string]int, len(m.ItemList))
	for i := range m.ItemList {
		m.index[m.ItemList[i].Path] = i
	}
}

// Get returns the entry for a file path.
func (m *ManifestObject) Get(path string) (*ManifestItem, bool) {
	path = normalizeManifestPath(path)
	if m.index == nil {
		m.buildIndex()
	}
	i, ok := m.index[path]
	if !ok {
		return nil, false
	}
	return &m.ItemList[i], true
}

// HasDir reports whether dir is an implied directory in the manifest.
func (m *ManifestObject) HasDir(dir string) bool {
	dir = normalizeManifestPath(dir)
	if dir == "" {
		return true
	}
	prefix := dir + "/"
	for i := range m.ItemList {
		if strings.HasPrefix(m.ItemList[i].Path, prefix) {
			return true
		}
	}
	return false
}

func normalizeManifestPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/")
}

// Children returns the direct entries of dir. Directory entries are inferred
// from the flat manifest and have no hash; file entries carry the content
// artifact hash.
func (m *ManifestObject) Children(dir string) []TreeObjectItem {
	dir = normalizeManifestPath(dir)
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	res := make([]TreeObjectItem, 0, 8)
	seenDir := make(map[string]bool)
	for i := range m.ItemList {
		item := &m.ItemList[i]
		if prefix != "" && !strings.HasPrefix(item.Path, prefix) {
			continue
		}
		rest := item.Path[len(prefix):]
		if rest == "" {
			continue
		}
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			name := rest[:slash]
			if seenDir[name] {
				continue
			}
			seenDir[name] = true
			res = append(res, TreeObjectItem{
				Mode:  FOSSIL_MODE_TREE,
				Name:  name,
				IsDir: true,
			})
		} else {
			res = append(res, TreeObjectItem{
				Mode: item.Mode,
				Name: rest,
				Hash: item.BlobId,
			})
		}
	}
	return res
}

func sortManifestItems(items []ManifestItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
}

// applyDelta applies the F cards of a delta manifest to a complete baseline.
func applyDelta(base *ManifestObject, art *Artifact) *ManifestObject {
	res := &ManifestObject{
		Id:       art.Id,
		CommitId: art.Id,
		Baseline: art.Baseline,
		rawData:  art.Raw,
	}
	res.ItemList = make([]ManifestItem, len(base.ItemList))
	copy(res.ItemList, base.ItemList)
	res.buildIndex()
	for _, fc := range art.Files {
		path := normalizeManifestPath(fc.Path)
		if path == "" {
			continue
		}
		if !fc.HasBlob {
			if i, ok := res.index[path]; ok {
				res.ItemList = append(res.ItemList[:i], res.ItemList[i+1:]...)
				res.buildIndex()
			}
			continue
		}
		item := ManifestItem{
			Path:    path,
			BlobId:  fc.BlobId,
			Flags:   fc.Perm,
			Mode:    fossilFlagToMode(fc.Perm),
			OldPath: fc.OldPath,
		}
		if i, ok := res.index[path]; ok {
			res.ItemList[i] = item
		} else {
			res.index[path] = len(res.ItemList)
			res.ItemList = append(res.ItemList, item)
		}
	}
	sortManifestItems(res.ItemList)
	res.buildIndex()
	return res
}

// fullManifestFromArtifact turns a baseline manifest artifact into a complete
// manifest.
func fullManifestFromArtifact(art *Artifact) *ManifestObject {
	res := &ManifestObject{
		Id:       art.Id,
		CommitId: art.Id,
		Baseline: art.Baseline,
		rawData:  art.Raw,
		ItemList: make([]ManifestItem, 0, len(art.Files)),
	}
	for _, fc := range art.Files {
		path := normalizeManifestPath(fc.Path)
		if path == "" || !fc.HasBlob {
			continue
		}
		res.ItemList = append(res.ItemList, ManifestItem{
			Path:    path,
			BlobId:  fc.BlobId,
			Flags:   fc.Perm,
			Mode:    fossilFlagToMode(fc.Perm),
			OldPath: fc.OldPath,
		})
	}
	sortManifestItems(res.ItemList)
	res.buildIndex()
	return res
}

func fossilFlagToMode(perm string) int {
	mode := FOSSIL_MODE_NORMAL_FILE
	for i := 0; i < len(perm); i++ {
		switch perm[i] {
		case 'x':
			mode = FOSSIL_MODE_EXECUTABLE_FILE
		case 'l':
			mode = FOSSIL_MODE_SYMBOLIC_LINK
		}
	}
	return mode
}
