package launch

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrOutside is a new-session path that resolves outside the home directory
// (Q52). Resume does not use this.
var ErrOutside = errors.New("outside home")

// ErrNotDir is a path that exists and is not a directory.
var ErrNotDir = errors.New("not a directory")

// ErrWide means the search stopped before it had covered the home directory.
var ErrWide = errors.New("search stopped early")

const (
	maxDepth   = 5
	walkBudget = 8000
)

// Search finds folders under home for a new session (Q52).
// An exact existing directory is returned alone, with no search.
// ~/x and x are relative to home. An absolute path outside home is read as
// relative to home (/opt/foo becomes ~/opt/foo). A path that still points
// outside home after .. and symlinks is ErrOutside.
// No match returns the path that would be created, which the bot only
// creates after a tap. More than eight matches is the caller's problem:
// this returns all of them.
func Search(home, query string) (matches []string, create string, err error) {
	home = filepath.Clean(home)
	resolved, err := interpret(home, query)
	if err != nil || resolved == "" {
		return nil, "", err
	}
	if fi, statErr := os.Stat(resolved); statErr == nil {
		if !fi.IsDir() {
			return nil, "", ErrNotDir
		}
		return []string{resolved}, "", nil
	}
	rel, err := filepath.Rel(home, resolved)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, "", ErrOutside
	}
	segs := splitSegs(rel)
	if len(segs) == 0 {
		return []string{home}, "", nil
	}
	budget := walkBudget
	var found []string
	walk(home, 0, segs, home, &found, &budget)
	sort.Strings(found)
	if budget <= 0 && len(found) == 0 {
		return nil, "", ErrWide
	}
	if len(found) == 0 {
		return nil, resolved, nil
	}
	return found, "", nil
}

func splitSegs(rel string) []string {
	var segs []string
	for _, s := range strings.Split(rel, string(os.PathSeparator)) {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	return segs
}

// interpret turns the typed folder into a cleaned path under home, or
// ErrOutside when it escapes.
func interpret(home, query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", nil
	}
	var path string
	switch {
	case q == "~":
		path = home
	case strings.HasPrefix(q, "~/"):
		path = filepath.Join(home, q[2:])
	case filepath.IsAbs(q):
		if inside(home, q) {
			path = q
		} else {
			path = filepath.Join(home, strings.TrimPrefix(filepath.Clean(q), string(os.PathSeparator)))
		}
	default:
		path = filepath.Join(home, q)
	}
	return resolve(home, filepath.Clean(path))
}

// resolve evaluates the longest existing prefix and refuses a result
// outside home.
func resolve(home, path string) (string, error) {
	if !inside(home, path) {
		return "", ErrOutside
	}
	cur := path
	var missing []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		if cur == home || cur == string(os.PathSeparator) {
			return "", ErrOutside
		}
		dir, base := filepath.Split(strings.TrimRight(cur, string(os.PathSeparator)))
		if base == "" {
			return "", ErrOutside
		}
		missing = append([]string{base}, missing...)
		cur = filepath.Clean(dir)
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	full := real
	for _, m := range missing {
		full = filepath.Join(full, m)
	}
	full = filepath.Clean(full)
	if !inside(home, full) {
		return "", ErrOutside
	}
	return full, nil
}

func inside(home, path string) bool {
	home, path = filepath.Clean(home), filepath.Clean(path)
	if path == home {
		return true
	}
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func walk(dir string, depth int, segs []string, home string, out *[]string, budget *int) {
	if depth >= maxDepth || *budget <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if *budget <= 0 {
			return
		}
		name := e.Name()
		if skipDir(name, segs[0]) {
			continue
		}
		next := filepath.Join(dir, name)
		if !dirEntry(next, e) {
			continue
		}
		*budget--
		if !safeDir(home, next) {
			continue
		}
		child := depth + 1
		if child > maxDepth {
			continue
		}
		if nameMatch(name, segs[0]) {
			if len(segs) == 1 {
				*out = append(*out, next)
			} else if child < maxDepth {
				matchRest(next, child, segs, 1, home, out, budget)
			}
		}
		if child < maxDepth {
			walk(next, child, segs, home, out, budget)
		}
	}
}

func matchRest(dir string, depth int, segs []string, si int, home string, out *[]string, budget *int) {
	if si >= len(segs) || depth >= maxDepth || *budget <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if *budget <= 0 {
			return
		}
		name := e.Name()
		if skipDir(name, segs[si]) {
			continue
		}
		next := filepath.Join(dir, name)
		if !dirEntry(next, e) {
			continue
		}
		*budget--
		if !safeDir(home, next) || !nameMatch(name, segs[si]) {
			continue
		}
		if si == len(segs)-1 {
			*out = append(*out, next)
			continue
		}
		if depth+1 < maxDepth {
			matchRest(next, depth+1, segs, si+1, home, out, budget)
		}
	}
}

func nameMatch(name, seg string) bool {
	return strings.Contains(strings.ToLower(name), strings.ToLower(seg))
}

func skipDir(name, seg string) bool {
	if name == "." || name == ".." {
		return true
	}
	if strings.HasPrefix(name, ".") && !strings.Contains(seg, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "__pycache__", "venv", ".venv":
		return true
	}
	return false
}

func dirEntry(path string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func safeDir(home, path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return inside(home, real)
}

// Short prints a path under home with a leading ~/.
func Short(home, path string) string {
	home, path = filepath.Clean(home), filepath.Clean(path)
	if path == home {
		return "~"
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return path
	}
	return "~/" + rel
}
