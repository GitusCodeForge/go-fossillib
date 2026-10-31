package fossillib

import (
	"crypto/sha1"
	"crypto/sha3"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireFossil(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("fossil"); err != nil {
		t.Skip("fossil executable is not installed")
	}
}

func newTestRepository(t *testing.T) *LocalFossilRepository {
	t.Helper()
	requireFossil(t)
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")
	repo, err := InitLocalFossilRepositoryWithOptions(repoPath, InitOptions{
		ProjectName:        "Test Project",
		ProjectDescription: "a test repository",
		AdminUser:          "tester",
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func addFile(t *testing.T, repo *LocalFossilRepository, branch, path, content, message string) string {
	t.Helper()
	hash, err := repo.AddFileToRepoString(branch, path,
		"Alice", "alice@example.com",
		"Alice", "alice@example.com",
		message, content)
	if err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	return hash
}

func TestProjectInfoAndInit(t *testing.T) {
	repo := newTestRepository(t)
	info, err := repo.GetProjectInfo()
	if err != nil {
		t.Fatalf("project info: %v", err)
	}
	if info.Name != "Test Project" {
		t.Errorf("project name = %q, want Test Project", info.Name)
	}
	if info.Description != "a test repository" {
		t.Errorf("description = %q", info.Description)
	}
	if info.ProjectCode == "" {
		t.Errorf("project code is empty")
	}
	if info.HashPolicy != 1 && info.HashPolicy != 2 {
		t.Errorf("hash policy = %d", info.HashPolicy)
	}
	if info.CheckinCount < 1 {
		t.Errorf("check-in count = %d", info.CheckinCount)
	}
}

func TestAddFilesAndReadTree(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "README.md", "# Hello\n", "add readme")
	hash, err := repo.AddMultipleFileToRepoString("trunk",
		"Bob", "bob@example.com", "Bob", "bob@example.com",
		"add source files",
		map[string]string{
			"src/main.go":     "package main\n\nfunc main() {}\n",
			"src/sub/util.go": "package sub\n",
			"docs/readme.txt": "docs\n",
		})
	if err != nil {
		t.Fatalf("add multiple: %v", err)
	}
	if hash == h1 {
		t.Fatalf("second commit did not create a new hash")
	}

	tip, err := repo.GetTip()
	if err != nil {
		t.Fatalf("tip: %v", err)
	}
	if tip.Id != hash {
		t.Errorf("tip = %s, want %s", tip.Id, hash)
	}
	if tip.CommitMessage != "add source files" {
		t.Errorf("message = %q", tip.CommitMessage)
	}
	if tip.AuthorInfo.AuthorName != "Bob" {
		t.Errorf("author = %q", tip.AuthorInfo.AuthorName)
	}

	manifest, err := repo.GetManifest(tip)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.ItemList) != 4 {
		t.Errorf("manifest has %d files, want 4", len(manifest.ItemList))
	}
	if _, ok := manifest.Get("src/sub/util.go"); !ok {
		t.Errorf("manifest is missing src/sub/util.go")
	}

	tree, err := repo.GetTree(tip, "")
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree.ObjectList) != 3 {
		t.Errorf("root has %d entries, want 3: %v", len(tree.ObjectList), tree.ObjectList)
	}
	src, err := repo.ResolveTreePath(tree, "src")
	if err != nil {
		t.Fatalf("resolve src: %v", err)
	}
	srcTree, ok := src.(*TreeObject)
	if !ok {
		t.Fatalf("src is %T, want *TreeObject", src)
	}
	if len(srcTree.ObjectList) != 2 {
		t.Errorf("src has %d entries, want 2", len(srcTree.ObjectList))
	}
	blob, err := repo.ResolveTreePath(tree, "src/main.go")
	if err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	if string(blob.RawData()) != "package main\n\nfunc main() {}\n" {
		t.Errorf("file content = %q", blob.RawData())
	}
	if _, err := repo.ResolveTreePath(tree, "src/missing.go"); err != ErrObjectNotFound {
		t.Errorf("missing path error = %v", err)
	}

	files, err := repo.GetAllTrackedFiles()
	if err != nil {
		t.Fatalf("tracked files: %v", err)
	}
	if len(files) != 4 {
		t.Errorf("tracked files = %v", files)
	}
}

func TestCommitHistoryAndDiff(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "hello\nworld\n", "first")
	h2 := addFile(t, repo, "trunk", "a.txt", "hello\nworld\nmore\n", "second")

	history, err := repo.GetCommitHistoryN(h2, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("history has %d entries", len(history))
	}
	if history[0].Id != h2 || history[1].Id != h1 {
		t.Errorf("history order = %s, %s", history[0].Id, history[1].Id)
	}

	diff, err := repo.GetDiff(h2)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.CommitHash != h2 {
		t.Errorf("diff commit = %s", diff.CommitHash)
	}
	if len(diff.ItemList) != 1 {
		t.Fatalf("diff has %d items", len(diff.ItemList))
	}
	item := diff.ItemList[0]
	if item.File1 != "a.txt" {
		t.Errorf("diff file = %q", item.File1)
	}
	if len(item.PatchList) != 1 {
		t.Fatalf("diff has %d patches", len(item.PatchList))
	}
	if len(item.PatchList[0].LineList) == 0 {
		t.Errorf("diff hunk has no lines")
	}

	between, err := repo.GetDiffBetween(h1, h2)
	if err != nil {
		t.Fatalf("diff between: %v", err)
	}
	if len(between.ItemList) != 1 {
		t.Errorf("between diff has %d items", len(between.ItemList))
	}

	changes, err := repo.GetChangedFiles(&history[0])
	if err != nil {
		t.Fatalf("changed files: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "a.txt" || changes[0].State != "modified" {
		t.Errorf("changed files = %+v", changes)
	}
}

func TestBlame(t *testing.T) {
	repo := newTestRepository(t)
	addFile(t, repo, "trunk", "dir/hello.txt", "one\ntwo\n", "first")
	h2 := addFile(t, repo, "trunk", "dir/hello.txt", "one\ntwo\nthree\n", "second")

	blame, err := repo.BlameAt(h2, "dir/hello.txt")
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	if len(blame.LineList) != 2 {
		t.Fatalf("blame has %d groups, want 2: %+v", len(blame.LineList), blame.LineList)
	}
	if len(blame.LineList[0]) != 2 {
		t.Errorf("first group has %d lines", len(blame.LineList[0]))
	}
	last := blame.LineList[len(blame.LineList)-1][0]
	info := blame.CommitInfo[last.CommitId]
	if info == nil || info.Summary != "second" {
		t.Errorf("last line info = %+v", info)
	}
	if last.Value != "three" {
		t.Errorf("last line = %q", last.Value)
	}
}

func TestLastCommitAtDir(t *testing.T) {
	repo := newTestRepository(t)
	addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	h2 := addFile(t, repo, "trunk", "dir/b.txt", "b\n", "second")
	tip, err := repo.GetTip()
	if err != nil {
		t.Fatalf("tip: %v", err)
	}
	last, err := repo.ResolveLastCommitAtDir(tip, "")
	if err != nil {
		t.Fatalf("last: %v", err)
	}
	if last["dir"] == "" {
		t.Errorf("directory has no last commit: %v", last)
	}
	if last["a.txt"] == "" {
		t.Errorf("a.txt has no last commit: %v", last)
	}
	sub, err := repo.ResolveLastCommitAtDir(tip, "dir")
	if err != nil {
		t.Fatalf("last dir: %v", err)
	}
	if sub["b.txt"] != h2 {
		t.Errorf("dir/b.txt last commit = %s, want %s", sub["b.txt"], h2)
	}
}

func TestBranchesAndTags(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "a\n", "first")

	branches, err := repo.GetAllBranchList()
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if b := branches["trunk"]; b == nil || b.HeadId != h1 {
		t.Errorf("trunk = %+v", b)
	} else if b.Comment == "" || b.Date.IsZero() {
		t.Errorf("trunk metadata is empty: %+v", b)
	}

	if err := repo.CreateBranch("feature", h1); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := repo.GetAllBranchList(); err != nil {
		t.Fatalf("reload branches: %v", err)
	}
	feature, err := repo.GetBranch("feature")
	if err != nil {
		t.Fatalf("get branch: %v", err)
	}
	if feature.HeadId == "" {
		t.Errorf("feature has no head")
	}
	if featureHead, err := repo.GetCommit(feature.HeadId); err != nil {
		t.Fatalf("feature head commit: %v", err)
	} else if featureHead.ParentId() != h1 {
		t.Errorf("feature parent = %s, want %s", featureHead.ParentId(), h1)
	}
	if err := repo.CloseBranch("feature"); err != nil {
		t.Fatalf("close branch: %v", err)
	}
	feature, err = repo.GetBranch("feature")
	if err != nil {
		t.Fatalf("get closed branch: %v", err)
	}
	if !feature.IsClosed {
		t.Errorf("feature is not closed")
	}
	if err := repo.ReopenBranch("feature"); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if err := repo.AddTag("v1.0", h1, ""); err != nil {
		t.Fatalf("add tag: %v", err)
	}
	tags, err := repo.GetAllTagList()
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	tag := tags["v1.0"]
	if tag == nil || tag.HeadId != h1 {
		t.Errorf("v1.0 = %+v", tag)
	}
	if b := tags["trunk"]; b == nil || !b.IsBranch {
		t.Errorf("trunk tag = %+v", b)
	}
	if err := repo.CancelTag("v1.0", h1); err != nil {
		t.Fatalf("cancel tag: %v", err)
	}
}

func TestSettingsHooksAndRemotes(t *testing.T) {
	repo := newTestRepository(t)
	if err := repo.SetSetting("autosync", "0"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	v, err := repo.GetSetting("autosync")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if v != "0" {
		t.Errorf("autosync = %q", v)
	}
	settings, err := repo.GetAllSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, ok := settings["autosync"]; !ok {
		t.Errorf("autosync missing from settings")
	}

	if err := repo.AddHook("echo hi", "before-commit", 1); err != nil {
		t.Fatalf("add hook: %v", err)
	}
	hooks, err := repo.GetHooks()
	if err != nil {
		t.Fatalf("hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].Type != "before-commit" || hooks[0].Command != "echo hi" {
		t.Errorf("hooks = %+v", hooks)
	}
	if err := repo.DeleteHook("all"); err != nil {
		t.Fatalf("delete hooks: %v", err)
	}

	if err := repo.SetRemote("origin", "https://example.com/repo.fossil"); err != nil {
		t.Fatalf("set remote: %v", err)
	}
	url, err := repo.GetRemoteURL("origin")
	if err != nil {
		t.Fatalf("get remote: %v", err)
	}
	if url != "https://example.com/repo.fossil" {
		t.Errorf("remote url = %q", url)
	}
	if ok, err := repo.HasRemote("origin"); err != nil || !ok {
		t.Errorf("has remote = %v, %v", ok, err)
	}
	if err := repo.DeleteRemote("origin"); err != nil {
		t.Fatalf("delete remote: %v", err)
	}
	if ok, _ := repo.HasRemote("origin"); ok {
		t.Errorf("remote still present")
	}
}

func TestClone(t *testing.T) {
	repo := newTestRepository(t)
	addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	dir := t.TempDir()
	clonePath := filepath.Join(dir, "clone.fossil")
	clone, err := CloneFossilRepository("file://"+repo.RepositoryPath, clonePath)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	defer clone.Close()
	tip, err := clone.GetTip()
	if err != nil {
		t.Fatalf("clone tip: %v", err)
	}
	content, err := clone.GetFileContent(tip, "a.txt")
	if err != nil {
		t.Fatalf("clone file: %v", err)
	}
	if string(content) != "a\n" {
		t.Errorf("clone content = %q", content)
	}
}

func TestDeltaManifestAndExecutableMode(t *testing.T) {
	requireFossil(t)
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "delta.fossil")
	if _, err := InitLocalFossilRepositoryWithOptions(repoPath, InitOptions{AdminUser: "tester"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runFossilIn(work, "open", repoPath, "--workdir", work, "--nosync"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "one.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runInCheckout(work, "add", "one.txt", "run.sh"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runInCheckout(work, "commit", "-m", "delta one", "--user-override", "Tester <t@example.com>", "--user", "tester", "--delta", "--no-warnings", "--nosync"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "two.txt"), []byte("two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runInCheckout(work, "add", "two.txt"); err != nil {
		t.Fatalf("add two: %v", err)
	}
	if _, err := runInCheckout(work, "commit", "-m", "delta two", "--user-override", "Tester <t@example.com>", "--user", "tester", "--delta", "--no-warnings", "--nosync"); err != nil {
		t.Fatalf("commit two: %v", err)
	}

	repo, err := NewLocalFossilRepository(repoPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	tip, err := repo.GetTip()
	if err != nil {
		t.Fatalf("tip: %v", err)
	}
	manifest, err := repo.GetManifest(tip)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.ItemList) != 3 {
		t.Fatalf("delta manifest resolved to %d files, want 3: %+v", len(manifest.ItemList), manifest.ItemList)
	}
	item, ok := manifest.Get("run.sh")
	if !ok {
		t.Fatalf("run.sh missing")
	}
	if item.Mode != FOSSIL_MODE_EXECUTABLE_FILE {
		t.Errorf("run.sh mode = %o", item.Mode)
	}
	if content, err := repo.GetFileContent(tip, "two.txt"); err != nil || string(content) != "two\n" {
		t.Errorf("two.txt = %q, %v", content, err)
	}
}

func TestCheckoutDiscovery(t *testing.T) {
	repo := newTestRepository(t)
	addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runFossilIn(work, "open", repo.RepositoryPath, "--workdir", work, "--nosync"); err != nil {
		t.Fatalf("open: %v", err)
	}
	handle, err := NewLocalFossilRepository(work)
	if err != nil {
		t.Fatalf("open checkout: %v", err)
	}
	if handle.RepositoryPath == "" {
		t.Errorf("repository path not discovered")
	}
	branch, err := handle.GetWorkingDirBranch()
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if branch != "trunk" {
		t.Errorf("branch = %q", branch)
	}
	version, parent, err := handle.GetWorkingDirParents()
	if err != nil {
		t.Fatalf("parents: %v", err)
	}
	if version == "" || parent == "" {
		t.Errorf("version/parent = %q/%q", version, parent)
	}
}

func TestMergeConflictCheck(t *testing.T) {
	repo := newTestRepository(t)
	base := addFile(t, repo, "trunk", "f.txt", "base\n", "base")
	if err := repo.CreateBranch("side", base); err != nil {
		t.Fatalf("create side: %v", err)
	}
	if _, err := repo.AddFileToRepoString("side", "f.txt",
		"Side", "side@example.com", "Side", "side@example.com", "side change", "side\n"); err != nil {
		t.Fatalf("side commit: %v", err)
	}
	if _, err := repo.AddFileToRepoString("trunk", "f.txt",
		"Main", "main@example.com", "Main", "main@example.com", "trunk change", "trunk\n"); err != nil {
		t.Fatalf("trunk commit: %v", err)
	}
	result, err := repo.CheckBranchMergeConflict("trunk", "side")
	if err != nil {
		t.Fatalf("check merge: %v", err)
	}
	if result.Successful {
		t.Errorf("conflict was not detected: %+v", result)
	}
	if len(result.FileInfo) != 1 || result.FileInfo[0].FileName != "f.txt" {
		t.Errorf("conflict files = %+v", result.FileInfo)
	}
	if err := repo.Merge("side", "trunk", "Merger", "merger@example.com"); err == nil {
		t.Errorf("merge with conflict returned nil")
	} else if !strings.Contains(err.Error(), "merge has conflicts") {
		t.Errorf("merge error = %v", err)
	}
}

func TestAddToNewBranch(t *testing.T) {
	repo := newTestRepository(t)
	base := addFile(t, repo, "trunk", "a.txt", "a\n", "base")
	featureHead, err := repo.AddFileToRepoString("feature", "feature.txt",
		"Feature", "feature@example.com", "Feature", "feature@example.com",
		"start feature", "feature\n")
	if err != nil {
		t.Fatalf("add to new branch: %v", err)
	}
	feature, err := repo.GetBranch("feature")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if feature.HeadId != featureHead {
		t.Errorf("feature head = %s, want %s", feature.HeadId, featureHead)
	}
	commit, err := repo.GetCommit(featureHead)
	if err != nil {
		t.Fatalf("feature commit: %v", err)
	}
	if commit.Branch != "feature" {
		t.Errorf("branch = %q", commit.Branch)
	}
	if commit.ParentId() != base {
		t.Errorf("parent = %s, want %s", commit.ParentId(), base)
	}
	trunk, err := repo.GetBranch("trunk")
	if err != nil {
		t.Fatalf("get trunk: %v", err)
	}
	if trunk.HeadId != base {
		t.Errorf("trunk moved to %s", trunk.HeadId)
	}
}

func TestHistoryFromNonHeadAndAllChangesets(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	h2 := addFile(t, repo, "trunk", "b.txt", "b\n", "second")

	fromFirst, err := repo.GetCommitHistoryN(h1, 0)
	if err != nil {
		t.Fatalf("history from first: %v", err)
	}
	if len(fromFirst) != 2 || fromFirst[0].Id != h1 {
		t.Errorf("history from first = %+v", fromFirst)
	}
	if fromFirst[0].Id == h2 {
		t.Errorf("history from first starts at the tip")
	}

	all, err := repo.GetAllChangesets()
	if err != nil {
		t.Fatalf("all changesets: %v", err)
	}
	seen := make(map[string]bool, len(all))
	for _, c := range all {
		seen[c.Id] = true
	}
	if !seen[h1] || !seen[h2] {
		t.Errorf("all changesets missing commits: %v", seen)
	}
}

func TestFindTag(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	if err := repo.AddTag("release-1", h1, ""); err != nil {
		t.Fatalf("add tag: %v", err)
	}
	found, err := repo.FindTag("release-1", 0)
	if err != nil {
		t.Fatalf("find tag: %v", err)
	}
	if len(found) != 1 || found[0] != h1 {
		t.Errorf("find tag = %v, want [%s]", found, h1)
	}
}

func TestCleanMerge(t *testing.T) {
	repo := newTestRepository(t)
	base := addFile(t, repo, "trunk", "a.txt", "a\n", "base")
	if err := repo.CreateBranch("side", base); err != nil {
		t.Fatalf("create side: %v", err)
	}
	sideHash, err := repo.AddFileToRepoString("side", "side.txt",
		"Side", "side@example.com", "Side", "side@example.com", "side file", "side\n")
	if err != nil {
		t.Fatalf("side commit: %v", err)
	}
	mainHash, err := repo.AddFileToRepoString("trunk", "main.txt",
		"Main", "main@example.com", "Main", "main@example.com", "main file", "main\n")
	if err != nil {
		t.Fatalf("trunk commit: %v", err)
	}
	result, err := repo.CheckBranchMergeConflict("trunk", "side")
	if err != nil {
		t.Fatalf("check merge: %v", err)
	}
	if !result.Successful {
		t.Fatalf("clean merge reported conflicts: %+v", result)
	}
	if err := repo.Merge("side", "trunk", "Merger", "merger@example.com"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	tip, err := repo.GetBranchTip("trunk")
	if err != nil {
		t.Fatalf("merged tip: %v", err)
	}
	if !tip.IsMerge() {
		t.Fatalf("merged tip is not a merge: %+v", tip.ParentIdList)
	}
	parents := map[string]bool{}
	for _, p := range tip.ParentIdList {
		parents[p] = true
	}
	if !parents[mainHash] || !parents[sideHash] {
		t.Errorf("merge parents = %v, want %s and %s", tip.ParentIdList, mainHash, sideHash)
	}
	if _, err := repo.GetFileContent(tip, "side.txt"); err != nil {
		t.Errorf("side.txt missing after merge: %v", err)
	}
	if _, err := repo.GetFileContent(tip, "main.txt"); err != nil {
		t.Errorf("main.txt missing after merge: %v", err)
	}
}

func TestIsEmptyAndReadBlob(t *testing.T) {
	repo := newTestRepository(t)
	if !repo.IsEmpty() {
		t.Errorf("fresh repository is not empty")
	}
	h1 := addFile(t, repo, "trunk", "a.txt", "hello\n", "first")
	if repo.IsEmpty() {
		t.Errorf("repository with a file is empty")
	}
	tip, err := repo.GetCommit(h1)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	manifest, err := repo.GetManifest(tip)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	item, ok := manifest.Get("a.txt")
	if !ok {
		t.Fatalf("a.txt missing")
	}
	blob, err := repo.ReadBlob(item.BlobId)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if string(blob.Data) != "hello\n" {
		t.Errorf("blob = %q", blob.Data)
	}
	if len(item.BlobId) == 64 {
		prefix := item.BlobId[:40]
		expanded, err := repo.ReadBlob(prefix)
		if err != nil {
			t.Fatalf("read blob by prefix: %v", err)
		}
		if expanded.Id != item.BlobId {
			t.Errorf("expanded blob id = %s, want %s", expanded.Id, item.BlobId)
		}
	}
}

func TestGetTagsAt(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	if err := repo.AddTag("v1.0", h1, ""); err != nil {
		t.Fatalf("add tag: %v", err)
	}
	commit, err := repo.GetCommit(h1)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	tags, err := repo.GetTagsAt(commit)
	if err != nil {
		t.Fatalf("tags at: %v", err)
	}
	if tags["v1.0"] == nil {
		t.Errorf("v1.0 missing: %+v", tags)
	}
	if tags["trunk"] == nil {
		t.Errorf("trunk missing: %+v", tags)
	}
}

func TestBinaryDiff(t *testing.T) {
	repo := newTestRepository(t)
	binary := string([]byte{0x00, 0x01, 0x02, 0x00, 0xff})
	h1, err := repo.AddMultipleFileToRepoString("trunk",
		"Bin", "bin@example.com", "Bin", "bin@example.com",
		"add binary", map[string]string{"data.bin": binary})
	if err != nil {
		t.Fatalf("add binary: %v", err)
	}
	diff, err := repo.GetDiff(h1)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.ItemList) != 1 {
		t.Fatalf("diff items = %d", len(diff.ItemList))
	}
	if !diff.ItemList[0].Binary {
		t.Errorf("binary file was not marked binary: %+v", diff.ItemList[0])
	}
}

func TestDiffPreservesCRLF(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "crlf.txt", "a\r\nb\r\n", "add crlf")
	diff, err := repo.GetDiff(h1)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.ItemList) != 1 || len(diff.ItemList[0].PatchList) != 1 {
		t.Fatalf("diff = %+v", diff.ItemList)
	}
	lines := diff.ItemList[0].PatchList[0].LineList
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Line != "a\r" || lines[1].Line != "b\r" {
		t.Errorf("CRLF content was not preserved: %q %q", lines[0].Line, lines[1].Line)
	}
}

func TestEditHookAndDefaultRemote(t *testing.T) {
	repo := newTestRepository(t)
	if err := repo.AddHook("echo one", "before-commit", 1); err != nil {
		t.Fatalf("add hook: %v", err)
	}
	hooks, err := repo.GetHooks()
	if err != nil || len(hooks) != 1 {
		t.Fatalf("hooks = %+v, %v", hooks, err)
	}
	if err := repo.EditHook(hooks[0].ID, "echo two", "", 2); err != nil {
		t.Fatalf("edit hook: %v", err)
	}
	hooks, err = repo.GetHooks()
	if err != nil || len(hooks) != 1 {
		t.Fatalf("hooks after edit = %+v, %v", hooks, err)
	}
	if hooks[0].Command != "echo two" || hooks[0].Sequence != 2 {
		t.Errorf("edited hook = %+v", hooks[0])
	}
	if err := repo.DeleteHook("all"); err != nil {
		t.Fatalf("delete hooks: %v", err)
	}

	if err := repo.SetRemote("default", "https://example.com/default.fossil"); err != nil {
		t.Fatalf("set default remote: %v", err)
	}
	url, err := repo.GetRemoteURL("default")
	if err != nil {
		t.Fatalf("get default remote: %v", err)
	}
	if url != "https://example.com/default.fossil" {
		t.Errorf("default remote = %q", url)
	}
	remotes, err := repo.GetAllRemote()
	if err != nil {
		t.Fatalf("all remotes: %v", err)
	}
	if !containsString(remotes, "default") {
		t.Errorf("default missing from %v", remotes)
	}
}

func TestBranchCommitHistory(t *testing.T) {
	repo := newTestRepository(t)
	h1 := addFile(t, repo, "trunk", "a.txt", "a\n", "first")
	h2 := addFile(t, repo, "trunk", "b.txt", "b\n", "second")
	branch, err := repo.GetBranch("trunk")
	if err != nil {
		t.Fatalf("get branch: %v", err)
	}
	history, err := repo.GetBranchCommitHistoryN(*branch, 1)
	if err != nil {
		t.Fatalf("branch history: %v", err)
	}
	if len(history) != 1 || history[0].Id != h2 {
		t.Errorf("branch history = %+v", history)
	}
	all, err := repo.GetBranchCommitHistory(*branch)
	if err != nil {
		t.Fatalf("full branch history: %v", err)
	}
	if len(all) < 2 || all[0].Id != h2 || all[1].Id != h1 {
		t.Errorf("full branch history = %+v", all)
	}
}

func TestParseArtifactSignedManifest(t *testing.T) {
	hash := strings.Repeat("a", 64)
	body := "C hello\\sworld\n" +
		"D 2024-01-02T03:04:05.123\n" +
		"F dir/a.txt " + hash + " x\n" +
		"P " + strings.Repeat("b", 64) + "\n" +
		"T +sym-feature *\n" +
		"U Jane\\sDoe\\s<jane@example.com>\n" +
		"Z 00000000000000000000000000000000\n"
	signed := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA1\n\n" + body +
		"-----BEGIN PGP SIGNATURE-----\nVersion: GnuPG\n\nabcd\n-----END PGP SIGNATURE-----\n"
	art, err := ParseArtifact(hash, []byte(signed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if art.Kind != ArtifactCheckin {
		t.Errorf("kind = %v", art.Kind)
	}
	if art.Comment != "hello world" {
		t.Errorf("comment = %q", art.Comment)
	}
	if art.User != "Jane Doe <jane@example.com>" {
		t.Errorf("user = %q", art.User)
	}
	if len(art.Files) != 1 || art.Files[0].Path != "dir/a.txt" || art.Files[0].Perm != "x" {
		t.Errorf("files = %+v", art.Files)
	}
	if len(art.Parents) != 1 || art.Parents[0] != strings.Repeat("b", 64) {
		t.Errorf("parents = %v", art.Parents)
	}
	if art.Signature == "" {
		t.Errorf("signature was not retained")
	}
	if art.Date.IsZero() {
		t.Errorf("date was not parsed")
	}
}

func TestArtifactFullHash(t *testing.T) {
	raw := []byte("hello\n")
	sha1sum := sha1.Sum(raw)
	sha1hex := hex.EncodeToString(sha1sum[:])
	sha3sum := sha3.Sum256(raw)
	sha3hex := hex.EncodeToString(sha3sum[:])
	for _, test := range []struct{ prefix, want string }{
		{sha1hex, sha1hex},
		{sha1hex[:10], sha1hex},
		{sha3hex, sha3hex},
		{sha3hex[:10], sha3hex},
		{sha3hex[:40], sha3hex},
	} {
		if got := artifactFullHash(raw, test.prefix); got != test.want {
			t.Errorf("artifactFullHash(%q) = %q, want %q", test.prefix, got, test.want)
		}
	}
	if got := artifactFullHash(raw, "not-hex"); got != "" {
		t.Errorf("invalid prefix = %q", got)
	}
}

func TestDiffMultipleHunks(t *testing.T) {
	repo := newTestRepository(t)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	addFile(t, repo, "trunk", "many.txt", b.String(), "add many")
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	lines[0] = "changed first"
	lines[len(lines)-1] = "changed last"
	h := addFile(t, repo, "trunk", "many.txt", strings.Join(lines, "\n")+"\n", "change ends")
	diff, err := repo.GetDiff(h)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(diff.ItemList) != 1 {
		t.Fatalf("items = %d", len(diff.ItemList))
	}
	if len(diff.ItemList[0].PatchList) < 2 {
		t.Fatalf("hunks = %d, want at least 2", len(diff.ItemList[0].PatchList))
	}
}
