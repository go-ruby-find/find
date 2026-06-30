package find_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-ruby-find/find"
)

// osLister is a real-filesystem Lister, the kind the host (rbgo) would inject,
// binding onto File.exist?, File.lstat(...).directory? and Dir.children. It is
// used only by the differential MRI-oracle test below, which runs where a ruby
// binary is available (and never on Windows — the test skips there). The
// deterministic, ruby-free tests in find_test.go keep coverage at 100% on their
// own, so the no-ruby CI lanes still pass the gate without this file's body
// running.
type osLister struct{ root string }

func (osLister) Exist(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (osLister) IsDir(path string) (bool, error) {
	fi, err := os.Lstat(path) // Lstat: do not follow symlinks, matching File.lstat.
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

func (osLister) Children(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1) // base names, no "." / "..": Ruby Dir.children.
}

// rubyOrder runs `ruby -rfind` over the given roots and returns the visited
// paths in MRI's order.
func rubyOrder(t *testing.T, rubyBin string, roots ...string) []string {
	t.Helper()
	// Print each yielded path on its own line.
	script := `paths = ARGV; Find.find(*paths) { |p| STDOUT.puts(p) }`
	args := append([]string{"-rfind", "-e", script}, roots...)
	out, err := exec.Command(rubyBin, args...).Output()
	if err != nil {
		t.Fatalf("ruby failed: %v", err)
	}
	return splitLines(out)
}

func splitLines(b []byte) []string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// buildTree materialises a fixed synthetic tree under base and returns it.
func buildTree(t *testing.T, base string) {
	t.Helper()
	dirs := []string{
		"a/x", "a/y", "b", "c/deep/deeper",
	}
	files := []string{
		"Capital.txt", "zlast.txt",
		"a/top.txt", "a/x/1.txt", "a/x/2.txt", "a/y/m.txt",
		"b/file.txt", "c/deep/leaf",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		p := filepath.Join(base, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOracleAgainstMRI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("MRI oracle: skipped on Windows")
	}
	rubyBin, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("MRI oracle: ruby not found")
	}

	base := t.TempDir()
	buildTree(t, base)
	root := filepath.Join(base, "tree-root")
	if err := os.Rename(base, root); err == nil {
		base = root
	}

	// Walk with our engine over the real tree, using the same path joiner ruby's
	// File.join uses on this platform (os.PathSeparator). On unix this is "/".
	var got []string
	werr := find.WalkJoin([]string{base}, osLister{}, func(p string) error {
		got = append(got, p)
		return nil
	}, true, func(dir, name string) string { return filepath.Join(dir, name) })
	if werr != nil {
		t.Fatalf("Walk error: %v", werr)
	}

	want := rubyOrder(t, rubyBin, base)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MRI order mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestOraclePruneAgainstMRI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("MRI oracle: skipped on Windows")
	}
	rubyBin, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("MRI oracle: ruby not found")
	}
	base := t.TempDir()
	buildTree(t, base)

	// Prune the "a" subdirectory in both engines and compare.
	var got []string
	werr := find.WalkJoin([]string{base}, osLister{}, func(p string) error {
		got = append(got, p)
		if filepath.Base(p) == "a" {
			fi, _ := os.Lstat(p)
			if fi != nil && fi.IsDir() {
				return find.ErrPrune
			}
		}
		return nil
	}, true, func(dir, name string) string { return filepath.Join(dir, name) })
	if werr != nil {
		t.Fatalf("Walk error: %v", werr)
	}

	// Print the path first, then prune — matching the Go callback above, which
	// records the path before returning ErrPrune. (MRI's Find.prune throws, so any
	// code after it in the block does not run; printing before the prune keeps the
	// two engines comparing the same yielded set.)
	script := `base = ARGV[0]
Find.find(base) do |p|
  STDOUT.puts(p)
  if File.directory?(p) && File.basename(p) == "a"
    Find.prune
  end
end`
	out, rerr := exec.Command(rubyBin, "-rfind", "-e", script, base).Output()
	if rerr != nil {
		t.Fatalf("ruby prune failed: %v", rerr)
	}
	want := splitLines(out)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MRI prune mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestOracleMissingPathAgainstMRI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("MRI oracle: skipped on Windows")
	}
	rubyBin, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("MRI oracle: ruby not found")
	}
	base := t.TempDir()
	missing := filepath.Join(base, "does-not-exist")

	// MRI raises Errno::ENOENT for a missing start path; our Walk returns a
	// *MissingPathError. Confirm ruby raises an ENOENT-class error.
	script := `begin
  Find.find(ARGV[0]) { |p| }
  STDOUT.puts "NOERR"
rescue Errno::ENOENT
  STDOUT.puts "ENOENT"
end`
	out, rerr := exec.Command(rubyBin, "-rfind", "-e", script, missing).Output()
	if rerr != nil {
		t.Fatalf("ruby missing-path failed: %v", rerr)
	}
	if strings.TrimSpace(string(out)) != "ENOENT" {
		t.Fatalf("ruby did not raise ENOENT: %q", out)
	}

	werr := find.Walk([]string{missing}, osLister{}, func(p string) error { return nil }, true)
	var mpe *find.MissingPathError
	if !asMissing(werr, &mpe) {
		t.Fatalf("want *MissingPathError, got %T: %v", werr, werr)
	}
}

func asMissing(err error, target **find.MissingPathError) bool {
	m, ok := err.(*find.MissingPathError)
	if ok {
		*target = m
	}
	return ok
}

// TestOracleSortStabilityHint guards the assumption that Go's sort.Strings and
// MRI's String#<=> agree byte-wise for the ASCII names we test, so the oracle
// comparison is meaningful.
func TestOracleSortStabilityHint(t *testing.T) {
	names := []string{"Capital.txt", "a", "b", "c", "zlast.txt"}
	cp := append([]string(nil), names...)
	sort.Strings(cp)
	if !reflect.DeepEqual(cp, names) {
		t.Fatalf("ASCII order assumption broken: %v", cp)
	}
}
