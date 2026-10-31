package fossillib

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CommandError records a failed fossil invocation.
type CommandError struct {
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("fossil %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *CommandError) Unwrap() error { return e.Err }

// fossilEnv returns a stable environment for parsing text output.
func fossilEnv() []string {
	env := os.Environ()
	env = append(env,
		"LC_ALL=C",
		"TZ=UTC",
	)
	return env
}

// run invokes fossil with args followed by -R <repository>. Fossil accepts
// the repository option after the command and its subcommands, which keeps
// this helper independent of each command's option parser.
func (r *LocalFossilRepository) run(args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, args...)
	full = append(full, "-R", r.RepositoryPath)
	return runFossilIn(filepath.Dir(r.RepositoryPath), full...)
}

// runInCheckout invokes fossil with the working directory of a check-out.
// Some commands (annotate, merge, commit) only work inside an open check-out
// and must not receive -R.
func runInCheckout(dir string, args ...string) ([]byte, error) {
	return runFossilIn(dir, args...)
}

func runFossilIn(dir string, args ...string) ([]byte, error) {
	var lastOut []byte
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		cmd := exec.Command("fossil", args...)
		cmd.Dir = dir
		cmd.Env = fossilEnv()
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			return stdout.Bytes(), nil
		}
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		lastOut = stdout.Bytes()
		lastErr = &CommandError{
			Args:     args,
			ExitCode: code,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Err:      err,
		}
		if !isBusyCommandError(lastErr) {
			return lastOut, lastErr
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return lastOut, lastErr
}

// isBusyCommandError reports whether fossil failed because another process
// held the SQLite write lock. Such errors are safe to retry.
func isBusyCommandError(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	text := strings.ToLower(ce.Stdout + "\n" + ce.Stderr)
	return strings.Contains(text, "database is locked") ||
		strings.Contains(text, "sqlite_busy") ||
		strings.Contains(text, "is busy")
}

// jsonWarning is one entry of the warnings array of a fossil json reply.
type jsonWarning struct {
	Code int    `json:"code"`
	Text string `json:"text"`
}

// jsonEnvelope is the standard envelope of "fossil json".
type jsonEnvelope struct {
	Fossil     string          `json:"fossil"`
	Timestamp  int64           `json:"timestamp"`
	ResultCode string          `json:"resultCode"`
	ResultText string          `json:"resultText"`
	Command    string          `json:"command"`
	ProcTimeUS int64           `json:"procTimeUs"`
	ProcTimeMS int64           `json:"procTimeMs"`
	Payload    json.RawMessage `json:"payload"`
	Warnings   []jsonWarning   `json:"warnings"`
}

// runJSON invokes "fossil json <sub...>" and returns the payload. The
// subcommand arguments must be supplied after the final subcommand; run
// appends -R at the end, which is where fossil's json dispatcher expects it.
func (r *LocalFossilRepository) runJSON(sub ...string) ([]byte, error) {
	args := make([]string, 0, len(sub)+1)
	args = append(args, "json")
	args = append(args, sub...)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	var env jsonEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("%w: fossil json %s returned malformed JSON: %v", ErrInvalidFormat, strings.Join(sub, " "), err)
	}
	if env.ResultCode != "" && env.ResultCode != "FOSSIL-0000" {
		text := env.ResultText
		if text == "" {
			text = "unknown error"
		}
		return nil, fmt.Errorf("fossil json %s: %s (%s)", strings.Join(sub, " "), text, env.ResultCode)
	}
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		return nil, nil
	}
	return env.Payload, nil
}

var (
	hexHashRE    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	reNewVersion = regexp.MustCompile(`(?m)^New_Version:\s+([0-9a-fA-F]+)\s*$`)
)

// IsValidHash reports whether s is a full Fossil artifact hash: 40 hex
// characters for the old SHA1 policy or 64 for SHA3-256.
func IsValidHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return hexHashRE.MatchString(s)
}

// IsValidHashPrefix reports whether s could be a unique prefix of an
// artifact hash. Fossil accepts prefixes of four or more characters.
func IsValidHashPrefix(s string) bool {
	return len(s) >= 4 && len(s) <= 64 && hexHashRE.MatchString(s)
}

func normalizeHash(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// validBranchName applies the conservative subset of Fossil branch names
// that is safe to pass as a single command line argument.
func validBranchName(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == '/' || r == '+':
		default:
			return false
		}
	}
	return true
}

// validRepoPath reports whether p is a repository-root-relative path.
func validRepoPath(p string) bool {
	if p == "" || len(p) > 4096 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return false
	}
	if strings.ContainsAny(p, "\x00\r\n\\") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validUserPart(s string) bool {
	return !strings.ContainsAny(s, "\x00\r\n")
}

func formatFossilUser(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case email != "":
		return email
	default:
		return name
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// parseInt parses a decimal integer, returning 0 on failure.
func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// parallelMap runs fn over items with at most workers goroutines and returns
// the results in input order. The first error cancels outstanding work.
func parallelMap[T any](items []T, workers int, fn func(T) (string, error)) ([]string, error) {
	if workers < 1 {
		workers = 1
	}
	if len(items) == 0 {
		return nil, nil
	}
	if workers > len(items) {
		workers = len(items)
	}
	results := make([]string, len(items))
	errs := make([]error, len(items))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx], errs[idx] = fn(items[idx])
			}
		}()
	}
	for i := range items {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return results, err
		}
	}
	return results, nil
}
