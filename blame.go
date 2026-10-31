package fossillib

import (
	"fmt"
	"regexp"
	"strings"
)

// PorcelainBlameCommitInfo is the check-in information annotation reports for
// one line.
type PorcelainBlameCommitInfo struct {
	CommitId      string
	AuthorInfo    AuthorTime
	CommitterInfo AuthorTime
	Summary       string
	Filename      string
}

// PorcelainBlameLine is one annotated line.
type PorcelainBlameLine struct {
	CommitId        string
	Value           string
	FinalLineNumber int
}

// PorcelainBlame is the result of annotating a file. LineList groups
// consecutive lines that come from the same check-in.
type PorcelainBlame struct {
	CommitInfo map[string]*PorcelainBlameCommitInfo
	LineList   [][]*PorcelainBlameLine
}

var reAnnotateLine = regexp.MustCompile(`^([0-9a-fA-F]{4,64})\s+(\d{4}-\d{2}-\d{2})(?:\s+\S+)?\s+(\d+):\s?(.*)$`)

// Blame annotates a file at a check-in. Fossil's annotation engine only runs
// inside an open check-out, so this opens a temporary --keep check-out,
// materializes the one requested file and runs "fossil annotate" there.
func (r *LocalFossilRepository) Blame(c *CommitObject, filePath string) (*PorcelainBlame, error) {
	if c == nil {
		return nil, fmt.Errorf("no check-in given")
	}
	return r.BlameAt(c.Id, filePath)
}

// BlameAt is Blame for a check-in hash or prefix.
func (r *LocalFossilRepository) BlameAt(id, filePath string) (*PorcelainBlame, error) {
	if !validRepoPath(filePath) {
		return nil, fmt.Errorf("invalid file path %q", filePath)
	}
	version, err := r.resolveHash(id)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = r.withTempCheckout(version, true, func(dir string) error {
		if err := r.materializeFiles(dir, version, filePath); err != nil {
			return err
		}
		var err error
		out, err = runInCheckout(dir, "annotate", "-r", version, filePath)
		return err
	})
	if err != nil {
		return nil, err
	}
	records := parseAnnotateRecords(string(out))
	prefixes := make([]string, 0, len(records))
	for _, rec := range records {
		prefixes = append(prefixes, rec.prefix)
	}
	return r.buildBlame(records, filePath, r.resolveHashPrefixes(prefixes)), nil
}

// BlameAtVersion is an alias for BlameAt.
func (r *LocalFossilRepository) BlameAtVersion(id, filePath string) (*PorcelainBlame, error) {
	return r.BlameAt(id, filePath)
}

type annotateRecord struct {
	prefix string
	value  string
	line   int
}

func parseAnnotateRecords(text string) []annotateRecord {
	res := make([]annotateRecord, 0, 64)
	for _, raw := range strings.Split(text, "\n") {
		// Keep a trailing CR: it is part of the file content for a CRLF
		// file. The annotation prefix regex still matches.
		m := reAnnotateLine.FindStringSubmatch(raw)
		if len(m) == 0 {
			continue
		}
		res = append(res, annotateRecord{
			prefix: normalizeHash(m[1]),
			value:  m[4],
			line:   parseInt(m[3]),
		})
	}
	return res
}

func (r *LocalFossilRepository) buildBlame(records []annotateRecord, filePath string, resolved map[string]string) *PorcelainBlame {
	res := &PorcelainBlame{
		CommitInfo: make(map[string]*PorcelainBlameCommitInfo),
		LineList:   make([][]*PorcelainBlameLine, 0),
	}
	var current []*PorcelainBlameLine
	lastCommit := ""
	for _, rec := range records {
		full := resolved[rec.prefix]
		if full == "" {
			full = rec.prefix
		}
		info := res.CommitInfo[full]
		if info == nil {
			info = &PorcelainBlameCommitInfo{CommitId: full, Filename: filePath}
			if c, err := r.loadCommit(full, false); err == nil {
				info.AuthorInfo = c.AuthorInfo
				info.CommitterInfo = c.CommitterInfo
				info.Summary = firstLine(c.CommitMessage)
			}
			res.CommitInfo[full] = info
		}
		pl := &PorcelainBlameLine{
			CommitId:        full,
			Value:           rec.value,
			FinalLineNumber: rec.line,
		}
		if full != lastCommit {
			if len(current) > 0 {
				res.LineList = append(res.LineList, current)
			}
			current = make([]*PorcelainBlameLine, 0)
			lastCommit = full
		}
		current = append(current, pl)
	}
	if len(current) > 0 {
		res.LineList = append(res.LineList, current)
	}
	return res
}
