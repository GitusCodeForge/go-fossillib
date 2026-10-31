package fossillib

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MergeCheckConflictedFileInfo describes one file left unresolved by a merge.
// Fossil has no index stages, so Mode, ObjectId and Stage are zero/empty;
// FileName is the useful field.
type MergeCheckConflictedFileInfo struct {
	Mode     int    `json:"mode"`
	ObjectId string `json:"oid"`
	Stage    int    `json:"stage"`
	FileName string `json:"fileName"`
}

// MergeCheckInformationalMessage is one message produced while checking a
// merge.
type MergeCheckInformationalMessage struct {
	Path    []string `json:"path"`
	Type    string   `json:"type"`
	Message string   `json:"msg"`
}

// MergeCheckResult is the outcome of CheckBranchMergeConflict.
type MergeCheckResult struct {
	Successful         bool                             `json:"success"`
	ReceiverLocation   string                           `json:"receiverLocation"`
	ReceiverBranch     string                           `json:"receiverBranch"`
	ProviderRemoteName string                           `json:"providerRemoteName"`
	ProviderBranch     string                           `json:"providerBranch"`
	ToplevelTreeOid    string                           `json:"rootOid"`
	FileInfo           []MergeCheckConflictedFileInfo   `json:"fileInfo"`
	Message            []MergeCheckInformationalMessage `json:"msg"`
}

var (
	reConflictFile   = regexp.MustCompile(`(?m)^\*\*\*\*\*\s+\d+\s+merge conflicts?\s+in\s+(.+)$`)
	reConflictCount  = regexp.MustCompile(`(?m)^WARNING:\s+\d+\s+merge conflicts?`)
	reChangeConflict = regexp.MustCompile(`(?m)^CONFLICT\s+(.+)$`)
)

func (r *LocalFossilRepository) findBranch(name string) (*Branch, error) {
	hash, ok, err := r.resolveBranchHead(name)
	if err != nil {
		return nil, err
	}
	if !ok || hash == "" {
		return nil, fmt.Errorf("%w: branch %q", ErrObjectNotFound, name)
	}
	return &Branch{Name: name, HeadId: hash, HeadIds: []string{hash}}, nil
}

// CheckBranchMergeConflict runs a dry-run merge in a temporary full
// check-out and reports whether merging otherBranch into localBranch would
// leave conflicts.
func (r *LocalFossilRepository) CheckBranchMergeConflict(localBranch, otherBranch string) (*MergeCheckResult, error) {
	local, err := r.findBranch(localBranch)
	if err != nil {
		return nil, err
	}
	other, err := r.findBranch(otherBranch)
	if err != nil {
		return nil, err
	}
	res := &MergeCheckResult{
		Successful:       true,
		ReceiverLocation: local.HeadId,
		ReceiverBranch:   localBranch,
		ProviderBranch:   otherBranch,
		ToplevelTreeOid:  local.HeadId,
	}
	if local.HeadId == other.HeadId {
		return res, nil
	}

	var output string
	err = r.withTempCheckout(local.HeadId, false, func(dir string) error {
		out, err := runInCheckout(dir, "merge", "--dry-run", other.HeadId, "--nosync")
		if err != nil {
			return err
		}
		output = string(out)
		return nil
	})
	if err != nil {
		return nil, err
	}

	conflictFiles := make(map[string]bool)
	for _, m := range reConflictFile.FindAllStringSubmatch(output, -1) {
		name := strings.TrimSpace(m[1])
		if name != "" {
			conflictFiles[name] = true
		}
	}
	if reConflictCount.MatchString(output) {
		res.Successful = false
	} else if len(conflictFiles) > 0 {
		res.Successful = false
	}
	res.FileInfo = make([]MergeCheckConflictedFileInfo, 0, len(conflictFiles))
	for name := range conflictFiles {
		res.FileInfo = append(res.FileInfo, MergeCheckConflictedFileInfo{FileName: name})
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		msg := MergeCheckInformationalMessage{Type: "merge", Message: line}
		if m := reConflictFile.FindStringSubmatch(line); len(m) > 0 {
			msg.Path = []string{strings.TrimSpace(m[1])}
		}
		res.Message = append(res.Message, msg)
	}
	return res, nil
}

// Merge merges otherBranch into localBranch and commits the result. A full
// temporary check-out is unavoidable: Fossil's merge engine operates on a
// working tree. The repository's own check-out is never touched.
func (r *LocalFossilRepository) Merge(otherBranch, localBranch, author, email string) error {
	local, err := r.findBranch(localBranch)
	if err != nil {
		return err
	}
	other, err := r.findBranch(otherBranch)
	if err != nil {
		return err
	}
	if local.HeadId == other.HeadId {
		return nil
	}
	if !validUserPart(author) || !validUserPart(email) {
		return errors.New("invalid merge author")
	}
	user := formatFossilUser(author, email)
	if user == "" {
		return errors.New("no merge author")
	}

	return r.withTempCheckout(local.HeadId, false, func(dir string) error {
		out, err := runInCheckout(dir, "merge", other.HeadId, "--nosync")
		if err != nil {
			return err
		}
		mergeOutput := string(out)

		changes, err := runInCheckout(dir, "changes")
		if err != nil {
			return err
		}
		changesText := string(changes)
		conflicts := make([]string, 0)
		for _, m := range reChangeConflict.FindAllStringSubmatch(changesText, -1) {
			conflicts = append(conflicts, strings.TrimSpace(m[1]))
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("%w: %s", ErrMergeConflict, strings.Join(conflicts, ", "))
		}
		if strings.Contains(strings.ToLower(mergeOutput), "nothing to merge") ||
			strings.TrimSpace(changesText) == "" {
			return nil
		}

		msg := fmt.Sprintf("Merge %s into %s", otherBranch, localBranch)
		args := []string{"commit",
			"-m", msg,
			"--user-override", user,
			"--no-warnings",
			"--no-prompt",
			"--no-verify-comment",
			"--nosync"}
		r.mu.RLock()
		login := r.CommitUser
		r.mu.RUnlock()
		if login != "" {
			args = append(args, "--user", login)
		}
		commitOut, err := runInCheckout(dir, args...)
		if err != nil && isUserResolutionError(err) {
			if login = r.chooseCommitUser(); login != "" {
				retry := append(append([]string(nil), args...), "--user", login)
				commitOut, err = runInCheckout(dir, retry...)
			}
		}
		if err != nil {
			return err
		}
		if m := reNewVersion.FindSubmatch(commitOut); len(m) < 2 {
			return fmt.Errorf("fossil commit did not report New_Version: %s", strings.TrimSpace(string(commitOut)))
		}
		r.invalidateAfterWrite()
		return nil
	})
}
