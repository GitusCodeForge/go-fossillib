package fossillib

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// benchRepository builds a repository with n commits using the fossil
// executable directly. It is shared by the benchmarks below.
func hasFossil() bool {
	_, err := exec.LookPath("fossil")
	return err == nil
}

func benchRepository(b *testing.B, n int) (*LocalFossilRepository, string, string) {
	b.Helper()
	if !hasFossil() {
		b.Skip("fossil executable is not installed")
	}
	dir := b.TempDir()
	repoPath := filepath.Join(dir, "bench.fossil")
	if _, err := InitLocalFossilRepositoryWithOptions(repoPath, InitOptions{AdminUser: "tester"}); err != nil {
		b.Fatalf("init: %v", err)
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		b.Fatal(err)
	}
	if _, err := runFossilIn(work, "open", repoPath, "--workdir", work, "--nosync"); err != nil {
		b.Fatalf("open: %v", err)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("dir/file%03d.txt", i)
		if err := os.MkdirAll(filepath.Join(work, "dir"), 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, name), []byte(fmt.Sprintf("content %d\n", i)), 0644); err != nil {
			b.Fatal(err)
		}
		if _, err := runInCheckout(work, "add", name); err != nil {
			b.Fatalf("add %d: %v", i, err)
		}
		if _, err := runInCheckout(work, "commit", "-m", fmt.Sprintf("commit %d", i),
			"--user-override", "Bench <bench@example.com>", "--user", "tester",
			"--no-warnings", "--nosync"); err != nil {
			b.Fatalf("commit %d: %v", i, err)
		}
	}
	repo, err := NewLocalFossilRepository(repoPath)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	tip, err := repo.getTipHash()
	if err != nil {
		b.Fatalf("tip: %v", err)
	}
	return repo, tip, nameFile(n)
}

func nameFile(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("dir/file%03d.txt", n-1)
}

func BenchmarkGetCommitHistoryN(b *testing.B) {
	repo, tip, _ := benchRepository(b, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetCommitHistoryN(tip, 30); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetTree(b *testing.B) {
	repo, _, _ := benchRepository(b, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repo.Refresh()
		if _, err := repo.GetTreeAt(mustTip(b, repo), ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetFileContent(b *testing.B) {
	repo, tip, file := benchRepository(b, 50)
	c, err := repo.GetCommit(tip)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetFileContent(c, file); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResolveLastCommitAtDir(b *testing.B) {
	repo, tip, _ := benchRepository(b, 50)
	c, err := repo.GetCommit(tip)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repo.ClearLastCommitCache()
		if _, err := repo.ResolveLastCommitAtDir(c, "dir"); err != nil {
			b.Fatal(err)
		}
	}
}

func mustTip(b *testing.B, repo *LocalFossilRepository) string {
	b.Helper()
	tip, err := repo.getTipHash()
	if err != nil {
		b.Fatal(err)
	}
	return tip
}
