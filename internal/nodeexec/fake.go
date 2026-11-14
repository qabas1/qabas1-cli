package nodeexec

import (
	"context"
	"io"
	"strings"
	"sync"
)

// Fake is a scriptable Executor for tests. Rules are matched in order by substring.
type Fake struct {
	Name  string
	mu    sync.Mutex
	rules []fakeRule
	Log   []string
	Files map[string]string
	// Scripts holds only the script argument (what would be visible in argv/ps).
	Scripts []string
}

type fakeRule struct {
	contains string
	result   Result
}

func NewFake(name string) *Fake { return &Fake{Name: name, Files: map[string]string{}} }

// On registers a canned result for scripts containing substr.
func (f *Fake) On(substr string, res Result) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{contains: substr, result: res})
	return f
}

// OnOut is shorthand for a successful script printing stdout.
func (f *Fake) OnOut(substr, stdout string) *Fake { return f.On(substr, Result{Stdout: stdout}) }

func (f *Fake) Node() string { return f.Name }
func (f *Fake) Close() error { return nil }

func (f *Fake) Run(_ context.Context, script string, stdin io.Reader) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scripts = append(f.Scripts, script)
	var in string
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	if in != "" && strings.Contains(script, ".qabas-tmp") {
		f.Log = append(f.Log, script)
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(line, "mv -f ") {
				parts := strings.Fields(line)
				f.Files[strings.Trim(parts[len(parts)-1], "'")] = in
			}
		}
		return Result{}, nil
	}
	// SQL and other payloads travel on stdin; rules match the script plus stdin.
	full := script
	if in != "" {
		full += "\n" + in
	}
	f.Log = append(f.Log, full)
	for _, r := range f.rules {
		if strings.Contains(full, r.contains) {
			return r.result, nil
		}
	}
	return Result{}, nil
}

// Ran reports whether any executed script contains substr.
func (f *Fake) Ran(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.Log {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
