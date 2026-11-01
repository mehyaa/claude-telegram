package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type runner struct {
	p        Paths
	in       *bufio.Reader
	inRaw    io.Reader
	out, err io.Writer
	yes      bool
	all      bool // every install step; pair may skip if already paired
}

func newRunner(in io.Reader, out, errw io.Writer, yes bool) (*runner, error) {
	p, err := Resolve()
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if errw == nil {
		errw = os.Stderr
	}
	return &runner{
		p:     p,
		in:    bufio.NewReader(in),
		inRaw: in,
		out:   out,
		err:   errw,
		yes:   yes,
	}, nil
}

func (r *runner) info(msg string) { fmt.Fprintf(r.out, "  %s\n", msg) }
func (r *runner) step(msg string) { fmt.Fprintf(r.out, "\n== %s\n", msg) }
func (r *runner) warn(msg string) { fmt.Fprintf(r.err, "  ! %s\n", msg) }

func (r *runner) confirm(q string) bool {
	if r.yes {
		return true
	}
	fmt.Fprintf(r.out, "  %s [Y/n] ", q)
	reply, err := r.in.ReadString('\n')
	if err != nil && reply == "" {
		return false
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return true
	}
	return strings.EqualFold(reply[:1], "y")
}

func (r *runner) confirmNo(q string) bool {
	if r.yes {
		return true
	}
	fmt.Fprintf(r.out, "  %s [y/N] ", q)
	reply, err := r.in.ReadString('\n')
	if err != nil && reply == "" {
		return false
	}
	reply = strings.TrimSpace(reply)
	return reply != "" && strings.EqualFold(reply[:1], "y")
}

func need(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is needed; install it first", name)
	}
	return nil
}

func pathOnPATH(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dest, mode)
	}
	return err
}

func fileMode(path string) os.FileMode {
	fi, err := os.Stat(path)
	if err != nil {
		return 0o600
	}
	return fi.Mode().Perm()
}

func mtime(path string) (int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return fi.ModTime().Unix(), true
}

func isTTY(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
