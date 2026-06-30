package find_test

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-ruby-find/find"
)

// memFS is a deterministic in-memory Lister: a map from a directory path to its
// child base names, plus a set of paths that exist. It needs no real filesystem,
// so every test built on it is fully deterministic and runs identically on every
// OS (including Windows) and under qemu — no ruby, no temp dirs.
type memFS struct {
	// dirs maps a directory path to its (unsorted) child base names. A path present
	// as a key is a directory; the engine must sort, so we deliberately store the
	// children out of order.
	dirs map[string][]string
	// files is the set of non-directory paths that exist.
	files map[string]bool
	// missing forces Exist to report false for these paths (to model a vanished
	// entry without removing it from the tree it was discovered in).
	missing map[string]bool
	// isDirErr / childrenErr inject per-path errors for the error-pass-through tests.
	isDirErr    map[string]error
	childrenErr map[string]error
}

func (m *memFS) Exist(path string) bool {
	if m.missing[path] {
		return false
	}
	if _, ok := m.dirs[path]; ok {
		return true
	}
	return m.files[path]
}

func (m *memFS) IsDir(path string) (bool, error) {
	if e := m.isDirErr[path]; e != nil {
		return false, e
	}
	_, ok := m.dirs[path]
	return ok, nil
}

func (m *memFS) Children(dir string) ([]string, error) {
	if e := m.childrenErr[dir]; e != nil {
		return nil, e
	}
	// Return a copy in the stored (intentionally unsorted) order.
	out := make([]string, len(m.dirs[dir]))
	copy(out, m.dirs[dir])
	return out, nil
}

// sampleFS mirrors the synthetic tree used to capture the MRI golden order:
//
//	tree
//	  Capital.txt
//	  a/{top.txt, x/{1.txt,2.txt}, y/m.txt}
//	  b/file.txt
//	  c/deep/{deeper/, leaf}
//	  zlast.txt
//
// Children are stored out of order so the engine's own sort is exercised.
func sampleFS() *memFS {
	return &memFS{
		dirs: map[string][]string{
			"tree":               {"zlast.txt", "a", "Capital.txt", "c", "b"},
			"tree/a":             {"y", "top.txt", "x"},
			"tree/a/x":           {"2.txt", "1.txt"},
			"tree/a/y":           {"m.txt"},
			"tree/b":             {"file.txt"},
			"tree/c":             {"deep"},
			"tree/c/deep":        {"leaf", "deeper"},
			"tree/c/deep/deeper": {},
		},
		files: map[string]bool{
			"tree/Capital.txt": true,
			"tree/a/top.txt":   true,
			"tree/a/x/1.txt":   true,
			"tree/a/x/2.txt":   true,
			"tree/a/y/m.txt":   true,
			"tree/b/file.txt":  true,
			"tree/c/deep/leaf": true,
			"tree/zlast.txt":   true,
		},
	}
}

// collect runs Walk and returns the visited paths.
func collect(t *testing.T, roots []string, m find.Lister, ignoreError bool) []string {
	t.Helper()
	var got []string
	err := find.Walk(roots, m, func(p string) error {
		got = append(got, p)
		return nil
	}, ignoreError)
	if err != nil {
		t.Fatalf("Walk returned error: %v", err)
	}
	return got
}

func TestWalkOrder(t *testing.T) {
	want := []string{
		"tree",
		"tree/Capital.txt",
		"tree/a",
		"tree/a/top.txt",
		"tree/a/x",
		"tree/a/x/1.txt",
		"tree/a/x/2.txt",
		"tree/a/y",
		"tree/a/y/m.txt",
		"tree/b",
		"tree/b/file.txt",
		"tree/c",
		"tree/c/deep",
		"tree/c/deep/deeper",
		"tree/c/deep/leaf",
		"tree/zlast.txt",
	}
	got := collect(t, []string{"tree"}, sampleFS(), true)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestWalkMultipleRoots(t *testing.T) {
	want := []string{
		"tree/a",
		"tree/a/top.txt",
		"tree/a/x",
		"tree/a/x/1.txt",
		"tree/a/x/2.txt",
		"tree/a/y",
		"tree/a/y/m.txt",
		"tree/b",
		"tree/b/file.txt",
	}
	got := collect(t, []string{"tree/a", "tree/b"}, sampleFS(), true)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("multi-root order mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestPruneDirectory(t *testing.T) {
	// Prune directory "tree/a": it is yielded but not descended into.
	want := []string{
		"tree",
		"tree/Capital.txt",
		"tree/a", // yielded, then pruned
		"tree/b",
		"tree/b/file.txt",
		"tree/c",
		"tree/c/deep",
		"tree/c/deep/deeper",
		"tree/c/deep/leaf",
		"tree/zlast.txt",
	}
	var got []string
	err := find.Walk([]string{"tree"}, sampleFS(), func(p string) error {
		got = append(got, p)
		if p == "tree/a" {
			return find.ErrPrune
		}
		return nil
	}, true)
	if err != nil {
		t.Fatalf("Walk error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prune order mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestPruneLeaf(t *testing.T) {
	// Pruning a leaf is a no-op on order (it has no subtree), but exercises the
	// prune branch on a non-directory path.
	got := collect(t, []string{"tree/b"}, sampleFS(), true)
	want := []string{"tree/b", "tree/b/file.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("setup order: got=%v want=%v", got, want)
	}
	var got2 []string
	err := find.Walk([]string{"tree/b"}, sampleFS(), func(p string) error {
		got2 = append(got2, p)
		if p == "tree/b/file.txt" {
			return find.ErrPrune
		}
		return nil
	}, true)
	if err != nil {
		t.Fatalf("Walk error: %v", err)
	}
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("leaf prune mismatch: got=%v want=%v", got2, want)
	}
}

func TestMissingStartPathRaises(t *testing.T) {
	var yielded bool
	err := find.Walk([]string{"tree", "nope"}, sampleFS(), func(p string) error {
		yielded = true
		return nil
	}, true)
	var mpe *find.MissingPathError
	if !errors.As(err, &mpe) {
		t.Fatalf("want *MissingPathError, got %T: %v", err, err)
	}
	if mpe.Path != "nope" {
		t.Errorf("MissingPathError.Path = %q, want %q", mpe.Path, "nope")
	}
	if yielded {
		t.Error("nothing should be yielded when a start path is missing")
	}
	if msg := mpe.Error(); !strings.Contains(msg, "nope") {
		t.Errorf("error message %q should mention the path", msg)
	}
}

func TestYieldErrorStopsWalk(t *testing.T) {
	boom := errors.New("boom")
	var count int
	err := find.Walk([]string{"tree"}, sampleFS(), func(p string) error {
		count++
		if p == "tree/a" {
			return boom
		}
		return nil
	}, true)
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	// tree, tree/Capital.txt, tree/a => 3 yields then stop.
	if count != 3 {
		t.Errorf("yields before stop = %d, want 3", count)
	}
}

func TestIsDirErrorIgnored(t *testing.T) {
	m := sampleFS()
	m.isDirErr = map[string]error{"tree/a": errors.New("eacces")}
	// With ignoreError, tree/a is yielded but its subtree skipped (the IsDir
	// failure is swallowed, just like prune for descent purposes).
	want := []string{
		"tree",
		"tree/Capital.txt",
		"tree/a",
		"tree/b",
		"tree/b/file.txt",
		"tree/c",
		"tree/c/deep",
		"tree/c/deep/deeper",
		"tree/c/deep/leaf",
		"tree/zlast.txt",
	}
	got := collect(t, []string{"tree"}, m, true)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("isDir-error-ignored mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestIsDirErrorPropagated(t *testing.T) {
	m := sampleFS()
	sentinel := errors.New("eacces")
	m.isDirErr = map[string]error{"tree/a": sentinel}
	err := find.Walk([]string{"tree"}, m, func(p string) error { return nil }, false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel propagated, got %v", err)
	}
}

func TestChildrenErrorIgnored(t *testing.T) {
	m := sampleFS()
	m.childrenErr = map[string]error{"tree/a": errors.New("eacces")}
	want := []string{
		"tree",
		"tree/Capital.txt",
		"tree/a", // directory, but children unreadable -> skipped
		"tree/b",
		"tree/b/file.txt",
		"tree/c",
		"tree/c/deep",
		"tree/c/deep/deeper",
		"tree/c/deep/leaf",
		"tree/zlast.txt",
	}
	got := collect(t, []string{"tree"}, m, true)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("children-error-ignored mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestChildrenErrorPropagated(t *testing.T) {
	m := sampleFS()
	sentinel := errors.New("eacces")
	m.childrenErr = map[string]error{"tree/a": sentinel}
	err := find.Walk([]string{"tree"}, m, func(p string) error { return nil }, false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel propagated, got %v", err)
	}
}

func TestEmptyRoots(t *testing.T) {
	got := collect(t, nil, sampleFS(), true)
	if got != nil {
		t.Errorf("no roots should yield nothing, got %v", got)
	}
}

func TestSingleFileRoot(t *testing.T) {
	got := collect(t, []string{"tree/zlast.txt"}, sampleFS(), true)
	if !reflect.DeepEqual(got, []string{"tree/zlast.txt"}) {
		t.Errorf("single file root: got %v", got)
	}
}

func TestEmptyDirectory(t *testing.T) {
	got := collect(t, []string{"tree/c/deep/deeper"}, sampleFS(), true)
	if !reflect.DeepEqual(got, []string{"tree/c/deep/deeper"}) {
		t.Errorf("empty dir: got %v", got)
	}
}

func TestJoin(t *testing.T) {
	cases := []struct{ dir, name, want string }{
		{"", "a", "a"},
		{"a", "b", "a/b"},
		{"a/", "b", "a/b"},
		{"/", "b", "/b"},
		{"tree/a", "x", "tree/a/x"},
	}
	for _, c := range cases {
		if got := find.Join(c.dir, c.name); got != c.want {
			t.Errorf("Join(%q,%q)=%q want %q", c.dir, c.name, got, c.want)
		}
	}
}

func TestWalkJoinCustomJoiner(t *testing.T) {
	// A custom joiner using "\\" lets the host reproduce a different File.join;
	// verify WalkJoin threads it through and the resulting order is consistent.
	m := &memFS{
		dirs: map[string][]string{
			`r`:   {"b", "a"},
			`r\a`: {},
			`r\b`: {},
		},
		files: map[string]bool{},
	}
	var got []string
	err := find.WalkJoin([]string{"r"}, m, func(p string) error {
		got = append(got, p)
		return nil
	}, true, func(dir, name string) string { return dir + `\` + name })
	if err != nil {
		t.Fatalf("WalkJoin error: %v", err)
	}
	want := []string{`r`, `r\a`, `r\b`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("custom-join order: got=%v want=%v", got, want)
	}
}

func TestPruneSignalError(t *testing.T) {
	// ErrPrune carries a stable, descriptive message (it is an error value the host
	// may inspect); exercise its Error method.
	if msg := find.ErrPrune.Error(); !strings.Contains(msg, "prune") {
		t.Errorf("ErrPrune.Error()=%q should mention prune", msg)
	}
}

// TestSortedByByteValue pins MRI's byte-wise sort: uppercase sorts before
// lowercase, digits before letters.
func TestSortedByByteValue(t *testing.T) {
	m := &memFS{
		dirs:  map[string][]string{"d": {"banana", "Apple", "9", "apple", "Banana"}},
		files: map[string]bool{"d/banana": true, "d/Apple": true, "d/9": true, "d/apple": true, "d/Banana": true},
	}
	got := collect(t, []string{"d"}, m, true)
	want := []string{"d", "d/9", "d/Apple", "d/Banana", "d/apple", "d/banana"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("byte sort: got=%v want=%v", got, want)
	}
	// sanity: Go's sort.Strings is the byte-wise order we asserted.
	probe := []string{"banana", "Apple", "9", "apple", "Banana"}
	sort.Strings(probe)
	if !reflect.DeepEqual(probe, []string{"9", "Apple", "Banana", "apple", "banana"}) {
		t.Fatalf("unexpected sort baseline: %v", probe)
	}
}
