package fossillib

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	DIFF_OLD_MODE            uint8 = 1
	DIFF_NEW_MODE            uint8 = 2
	DIFF_DELETED_FILE_MODE   uint8 = 3
	DIFF_NEW_FILE_MODE       uint8 = 4
	DIFF_COPY_FROM           uint8 = 5
	DIFF_COPY_TO             uint8 = 6
	DIFF_RENAME_FROM         uint8 = 7
	DIFF_RENAME_TO           uint8 = 8
	DIFF_SIMILARITY_INDEX    uint8 = 9
	DIFF_DISSIMILARITY_INDEX uint8 = 10
	DIFF_INDEX               uint8 = 11
)

const (
	APPEND uint8 = 1
	DELETE uint8 = 2
	CHANGE uint8 = 3
	SAME   uint8 = 4
)

// AnnotatedLine is one line of a hunk. F1LineNum refers to the left side and
// F2LineNum to the right side; the counter that does not apply still carries
// the line position at which the change happened.
type AnnotatedLine struct {
	Type      uint8  `json:"type"`
	F1LineNum int64  `json:"f1"`
	F2LineNum int64  `json:"f2"`
	Line      string `json:"line"`
}

// DiffItemHeaderItem is one extended header line of a file diff.
type DiffItemHeaderItem struct {
	Type uint8    `json:"type"`
	Args []string `json:"args"`
}

// DiffItemPatch is one hunk.
type DiffItemPatch struct {
	LStart      int64           `json:"lStart"`
	LLineCount  int64           `json:"lLineCount"`
	RStart      int64           `json:"rStart"`
	RLineCount  int64           `json:"rLineCount"`
	ContextLine string          `json:"ctx"`
	LineList    []AnnotatedLine `json:"lines"`
}

// DiffItem is the diff of one file. Fossil reports binary files with a
// message instead of hunks; Binary and Message expose that case.
type DiffItem struct {
	File1     string                `json:"file1"`
	File2     string                `json:"file2"`
	Header    []*DiffItemHeaderItem `json:"header"`
	PatchList []*DiffItemPatch      `json:"patchList"`
	Binary    bool                  `json:"binary,omitempty"`
	Message   string                `json:"message,omitempty"`
}

// Diff is the diff of one check-in or of two versions.
type Diff struct {
	CommitHash string      `json:"commit"`
	ItemList   []*DiffItem `json:"item"`
}

// DiffOptions controls how fossil diff is invoked.
type DiffOptions struct {
	// Context is the number of context lines. A negative value uses
	// Fossil's default.
	Context int
	// IncludeAddedDeleted emits the complete text of added and deleted
	// files. Without it Fossil prints only the file names.
	IncludeAddedDeleted bool
	// IgnoreWhitespace ignores all whitespace changes.
	IgnoreWhitespace bool
	// IgnoreTrailingWhitespace ignores changes at end of line.
	IgnoreTrailingWhitespace bool
}

// DefaultDiffOptions matches the behaviour of the Git and Mercurial helpers:
// added and deleted files include their contents.
func DefaultDiffOptions() DiffOptions {
	return DiffOptions{Context: -1, IncludeAddedDeleted: true}
}

var (
	reHunkHeader       = regexp.MustCompile(`^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@(.*)$`)
	reFossilDiffStatus = regexp.MustCompile(`^(ADDED|DELETED|UPDATED|EDITED|CHANGED|MERGED_WITH|CONFLICT|RENAMED|COPIED)\s+`)
)

func parseFossilDiff(data []byte) (*Diff, error) {
	lines := strings.Split(string(data), "\n")
	res := &Diff{ItemList: make([]*DiffItem, 0)}
	var cur *DiffItem
	var patch *DiffItemPatch
	var lf, rf int64

	flushPatch := func() {
		if patch != nil && cur != nil {
			cur.PatchList = append(cur.PatchList, patch)
		}
		patch = nil
	}
	flushItem := func() {
		flushPatch()
		if cur != nil {
			res.ItemList = append(res.ItemList, cur)
			cur = nil
		}
	}
	ensureItem := func() *DiffItem {
		if cur == nil {
			cur = &DiffItem{Header: make([]*DiffItemHeaderItem, 0), PatchList: make([]*DiffItemPatch, 0)}
		}
		return cur
	}

	for _, raw := range lines {
		line := raw
		trimmed := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(trimmed, "Index: ") {
			flushItem()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "Index: "))
			cur = &DiffItem{
				File1:     name,
				File2:     name,
				Header:    make([]*DiffItemHeaderItem, 0),
				PatchList: make([]*DiffItemPatch, 0),
			}
			continue
		}
		if strings.HasPrefix(trimmed, "====") {
			continue
		}
		if reFossilDiffStatus.MatchString(trimmed) {
			// Fossil -v prefixes added/deleted files with a status line
			// such as "ADDED   data.bin". It is not part of the patch.
			flushItem()
			continue
		}
		if strings.HasPrefix(trimmed, "====") {
			continue
		}
		if strings.HasPrefix(trimmed, "@@ ") {
			ensureItem()
			m := reHunkHeader.FindStringSubmatch(trimmed)
			if len(m) == 0 {
				return nil, fmt.Errorf("%w: malformed hunk header %q", ErrInvalidFormat, line)
			}
			flushPatch()
			patch = &DiffItemPatch{
				LStart:      parseInt64(m[1]),
				LLineCount:  1,
				RStart:      parseInt64(m[3]),
				RLineCount:  1,
				ContextLine: strings.TrimSpace(m[5]),
				LineList:    make([]AnnotatedLine, 0, 8),
			}
			if m[2] != "" {
				patch.LLineCount = parseInt64(m[2])
			}
			if m[4] != "" {
				patch.RLineCount = parseInt64(m[4])
			}
			lf, rf = patch.LStart, patch.RStart
			continue
		}

		if patch != nil {
			if trimmed == "" {
				flushPatch()
				continue
			}
			if strings.HasPrefix(trimmed, "\\") {
				continue
			}
			switch line[0] {
			case ' ':
				patch.LineList = append(patch.LineList, AnnotatedLine{
					Type: SAME, F1LineNum: lf, F2LineNum: rf, Line: line[1:],
				})
				lf++
				rf++
				continue
			case '-':
				patch.LineList = append(patch.LineList, AnnotatedLine{
					Type: DELETE, F1LineNum: lf, F2LineNum: rf, Line: line[1:],
				})
				lf++
				continue
			case '+':
				patch.LineList = append(patch.LineList, AnnotatedLine{
					Type: APPEND, F1LineNum: lf, F2LineNum: rf, Line: line[1:],
				})
				rf++
				continue
			}
			flushPatch()
		}

		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--- ") {
			it := ensureItem()
			it.File1 = cleanDiffPath(strings.TrimPrefix(trimmed, "--- "))
			continue
		}
		if strings.HasPrefix(trimmed, "+++ ") {
			it := ensureItem()
			it.File2 = cleanDiffPath(strings.TrimPrefix(trimmed, "+++ "))
			continue
		}
		it := ensureItem()
		if strings.Contains(trimmed, "binary files") || strings.HasPrefix(trimmed, "Binary files ") {
			it.Binary = true
			it.Message = trimmed
			continue
		}
		it.Header = append(it.Header, parseDiffHeaderLine(trimmed))
	}
	flushItem()
	return res, nil
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func cleanDiffPath(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func parseDiffHeaderLine(line string) *DiffItemHeaderItem {
	type mapping struct {
		prefix string
		typ    uint8
	}
	mappings := []mapping{
		{"old mode ", DIFF_OLD_MODE},
		{"new mode ", DIFF_NEW_MODE},
		{"deleted file mode ", DIFF_DELETED_FILE_MODE},
		{"new file mode ", DIFF_NEW_FILE_MODE},
		{"copy from ", DIFF_COPY_FROM},
		{"copy to ", DIFF_COPY_TO},
		{"rename from ", DIFF_RENAME_FROM},
		{"rename to ", DIFF_RENAME_TO},
		{"similarity index ", DIFF_SIMILARITY_INDEX},
		{"dissimilarity index ", DIFF_DISSIMILARITY_INDEX},
		{"index ", DIFF_INDEX},
	}
	for _, m := range mappings {
		if strings.HasPrefix(line, m.prefix) {
			return &DiffItemHeaderItem{Type: m.typ, Args: []string{strings.TrimPrefix(line, m.prefix)}}
		}
	}
	return &DiffItemHeaderItem{Type: 0, Args: []string{line}}
}

func (r *LocalFossilRepository) diffArgs(flag, value string, opts DiffOptions) []string {
	args := []string{"diff", "-i"}
	if flag != "" {
		args = append(args, flag, value)
	}
	if opts.IncludeAddedDeleted {
		args = append(args, "-v")
	}
	if opts.Context >= 0 {
		args = append(args, "-c", strconv.Itoa(opts.Context))
	}
	if opts.IgnoreWhitespace {
		args = append(args, "-w")
	}
	if opts.IgnoreTrailingWhitespace {
		args = append(args, "-Z")
	}
	return args
}

// GetDiff returns the diff of a check-in against its primary parent.
func (r *LocalFossilRepository) GetDiff(commitId string) (*Diff, error) {
	return r.GetDiffWithOptions(commitId, DefaultDiffOptions())
}

// GetDiffWithOptions is GetDiff with explicit options.
func (r *LocalFossilRepository) GetDiffWithOptions(commitId string, opts DiffOptions) (*Diff, error) {
	full, err := r.resolveHash(commitId)
	if err != nil {
		return nil, err
	}
	args := r.diffArgs("--checkin", full, opts)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	diff, err := parseFossilDiff(out)
	if err != nil {
		return nil, err
	}
	diff.CommitHash = full
	return diff, nil
}

// GetDiffCheckin is an alias for GetDiff.
func (r *LocalFossilRepository) GetDiffCheckin(commitId string) (*Diff, error) {
	return r.GetDiff(commitId)
}

// GetDiffBetween returns the diff between two check-ins.
func (r *LocalFossilRepository) GetDiffBetween(from, to string) (*Diff, error) {
	return r.GetDiffBetweenWithOptions(from, to, DefaultDiffOptions())
}

// GetDiffBetweenWithOptions is GetDiffBetween with explicit options.
func (r *LocalFossilRepository) GetDiffBetweenWithOptions(from, to string, opts DiffOptions) (*Diff, error) {
	args := r.diffArgs("", "", opts)
	args = append(args, "--from", from, "--to", to)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	diff, err := parseFossilDiff(out)
	if err != nil {
		return nil, err
	}
	if full, err := r.resolveHash(to); err == nil {
		diff.CommitHash = full
	} else {
		diff.CommitHash = normalizeHash(to)
	}
	return diff, nil
}
