package fossillib

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Hook is one Fossil hook. Fossil hooks are rows in the repository, not
// files in a hooks directory; Type is the hook event (before-commit,
// after-receive, ...) or "disabled".
type Hook struct {
	ID       int
	Type     string
	Command  string
	Sequence int
}

var (
	reHookStart    = regexp.MustCompile(`^\s*(\d+):\s*type\s*=\s*(.*)$`)
	reHookCommand  = regexp.MustCompile(`^\s*command\s*=\s*(.*)$`)
	reHookSequence = regexp.MustCompile(`^\s*sequence\s*=\s*(\d+)\s*$`)
)

// GetHooks lists the repository hooks.
func (r *LocalFossilRepository) GetHooks() ([]Hook, error) {
	out, err := r.run("hook", "list")
	if err != nil {
		return nil, err
	}
	res := make([]Hook, 0, 4)
	var cur *Hook
	flush := func() {
		if cur != nil {
			res = append(res, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := reHookStart.FindStringSubmatch(line); len(m) > 0 {
			flush()
			id, _ := strconv.Atoi(m[1])
			cur = &Hook{ID: id, Type: strings.TrimSpace(m[2])}
			continue
		}
		if cur == nil {
			continue
		}
		if m := reHookCommand.FindStringSubmatch(line); len(m) > 0 {
			cur.Command = strings.TrimSpace(m[1])
			continue
		}
		if m := reHookSequence.FindStringSubmatch(line); len(m) > 0 {
			cur.Sequence = parseInt(m[1])
		}
	}
	flush()
	return res, nil
}

// GetHook returns one hook by ID.
func (r *LocalFossilRepository) GetHook(id int) (*Hook, error) {
	hooks, err := r.GetHooks()
	if err != nil {
		return nil, err
	}
	for i := range hooks {
		if hooks[i].ID == id {
			cp := hooks[i]
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("%w: hook %d", ErrObjectNotFound, id)
}

// AddHook creates a hook. sequence may be zero to use Fossil's default.
func (r *LocalFossilRepository) AddHook(command, hookType string, sequence int) error {
	if command == "" || hookType == "" {
		return fmt.Errorf("hook command and type are required")
	}
	args := []string{"hook", "add", "--command", command, "--type", hookType}
	if sequence > 0 {
		args = append(args, "--sequence", strconv.Itoa(sequence))
	}
	_, err := r.run(args...)
	return err
}

// EditHook changes an existing hook. Empty fields are left unchanged.
func (r *LocalFossilRepository) EditHook(id int, command, hookType string, sequence int) error {
	args := []string{"hook", "edit"}
	if command != "" {
		args = append(args, "--command", command)
	}
	if hookType != "" {
		args = append(args, "--type", hookType)
	}
	if sequence > 0 {
		args = append(args, "--sequence", strconv.Itoa(sequence))
	}
	if len(args) == 2 {
		return fmt.Errorf("no changes requested")
	}
	args = append(args, strconv.Itoa(id))
	_, err := r.run(args...)
	return err
}

// DeleteHook removes a hook by ID, or all hooks when id is "all".
func (r *LocalFossilRepository) DeleteHook(id string) error {
	if id == "" {
		return fmt.Errorf("empty hook id")
	}
	_, err := r.run("hook", "delete", id)
	return err
}

// GetAllSetHooksName returns hook IDs as strings, mirroring the Mercurial
// helper's naming style.
func (r *LocalFossilRepository) GetAllSetHooksName() ([]string, error) {
	hooks, err := r.GetHooks()
	if err != nil {
		return nil, err
	}
	res := make([]string, len(hooks))
	for i := range hooks {
		res[i] = strconv.Itoa(hooks[i].ID)
	}
	return res, nil
}
