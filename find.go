// Package find is a pure-Go (CGO=0) reimplementation of the recursive-traversal
// algorithm of Ruby's standard library Find module (require "find"), faithful to
// MRI 4.0.5's lib/find.rb.
//
// It implements only the deterministic, interpreter-independent core: the
// top-down walk over a set of start paths, MRI's exact visit order, and the
// Find.prune control flow. The real filesystem access — Dir.children, the
// File.exist?/File.lstat directory test — is NOT done here; it is injected by the
// host (rbgo supplies Dir.children + File.directory?/lstat). The engine drives the
// depth-first search, the sort/queue ordering, the prune throw/catch semantics and
// the per-path error pass-through exactly as MRI does, so that binding it to a real
// filesystem reproduces Find.find observable behaviour.
//
// # MRI algorithm
//
// MRI's Find.find(*paths) first checks every start path with File.exist? and
// raises Errno::ENOENT for the first one missing. It then processes each start
// path through a FIFO queue: it shifts the front path, yields it, lstat's it, and
// if it is a directory lists its children, sorts them ascending (byte-wise), and
// unshifts them — in reverse order — onto the front of the queue. Because the
// children are reversed before being pushed to the front, shifting visits them in
// ascending order, descending into each subdirectory before its later siblings:
// a depth-first, ascending-sorted walk. Find.prune aborts the current path's
// catch(:prune) block, so a directory chosen for pruning is yielded but not
// descended into.
//
// Per-path errors (lstat or Dir.children failing on an entry reached mid-walk) are
// swallowed when ignoreError is true (MRI's default), skipping that entry; when
// false they propagate. A missing START path always raises, regardless of
// ignoreError.
package find

import "sort"

// Lister is the injected filesystem seam. The host (rbgo) supplies it, mapping
// onto Ruby's File/Dir operations:
//
//   - Exist reports whether a START path exists (Ruby File.exist?). A start path
//     for which Exist returns false makes Walk fail before any yield, mirroring
//     MRI raising Errno::ENOENT.
//   - Children lists the entries of dir, WITHOUT the "." and ".." pseudo-entries
//     and WITHOUT the dir prefix (Ruby Dir.children). isDir reports whether the
//     path is a directory to descend into (Ruby File.lstat(path).directory?). The
//     engine never calls Children unless isDir(path) returned true. Children/isDir
//     may return an error for an entry reached mid-walk (e.g. EACCES, ENOENT after
//     a race); Walk applies the ignoreError policy to it.
//
// Children returns entries in any order; Walk sorts them itself to match MRI, so
// the host need not sort. The returned slice is owned by Walk after the call.
type Lister interface {
	// Exist reports whether path exists. Called only for the start paths.
	Exist(path string) bool
	// IsDir reports whether path is a directory whose children should be walked.
	// Mirrors MRI's File.lstat(path).directory? — symlinks are NOT followed, so a
	// symlink to a directory must report false. err propagates the lstat failure
	// (e.g. a path that vanished mid-walk); IsDir is consulted for every yielded
	// path, including the start paths.
	IsDir(path string) (isDir bool, err error)
	// Children returns the entries of dir (Ruby Dir.children): base names only, no
	// "." / "..", in any order. Called only when IsDir(dir) was true.
	Children(dir string) (entries []string, err error)
}

// errPrune is the sentinel the yield callback returns to request that the current
// path's subtree be skipped — the Go analogue of Ruby's `throw :prune`, which
// Find.prune raises. It never escapes Walk.
type pruneSignal struct{}

func (pruneSignal) Error() string { return "find: prune" }

// ErrPrune, when returned by the yield callback, prunes the current path: the
// path has already been yielded, but if it is a directory it is not descended
// into. It is the engine's equivalent of calling Find.prune inside the block.
// (The host binds Find.prune to returning this from the callback.) Returning
// ErrPrune is the ONLY in-band control value; any other non-nil error from yield
// stops the walk and is returned to the caller.
var ErrPrune error = pruneSignal{}

// Join joins a directory and a child base name the way Ruby's File.join does for
// this one-segment case: a single "/" separator, but without doubling a slash
// when dir already ends in one (File.join("a/", "b") == "a/b"). The host may
// supply its own joiner via WalkJoin; this is the default used by Walk.
func Join(dir, name string) string {
	if dir == "" {
		return name
	}
	if dir[len(dir)-1] == '/' {
		return dir + name
	}
	return dir + "/" + name
}

// Walk performs MRI Find.find's traversal over roots, driving the injected lister
// and invoking yield for every visited path in MRI's exact order. It is the
// algorithmic core the host binds Find.find to.
//
// Order: each root is processed in turn; within a root, the root is yielded first,
// then a depth-first walk of its contents with children visited in ascending
// (byte-wise) order.
//
// Control flow: if yield returns ErrPrune, the current path is not descended into
// (it was already yielded) and the walk continues with the next queued path. If
// yield returns any other non-nil error, Walk stops immediately and returns it.
//
// Errors: every root is first checked with lister.Exist; the first missing root
// makes Walk return a *MissingPathError without yielding anything (MRI raises
// Errno::ENOENT). Once walking, an IsDir or Children error on an entry is
// swallowed (the entry is skipped) when ignoreError is true, and returned
// otherwise. A start path is never subject to ignoreError for its existence check,
// but an IsDir/Children failure on a start path while walking follows the
// ignoreError policy, as in MRI.
func Walk(roots []string, lister Lister, yield func(path string) error, ignoreError bool) error {
	return walk(roots, lister, yield, ignoreError, Join)
}

// WalkJoin is Walk with a caller-supplied path joiner, letting the host reproduce
// a non-default File.join (e.g. an alternate separator). join must combine a
// directory with a single child base name.
func WalkJoin(roots []string, lister Lister, yield func(path string) error, ignoreError bool, join func(dir, name string) string) error {
	return walk(roots, lister, yield, ignoreError, join)
}

// MissingPathError reports that a start path passed to Walk does not exist. It
// mirrors MRI raising Errno::ENOENT for a missing top-level path; the host maps it
// back to Errno::ENOENT.
type MissingPathError struct {
	// Path is the start path that did not exist.
	Path string
}

func (e *MissingPathError) Error() string {
	return "find: no such file or directory - " + e.Path
}

func walk(roots []string, lister Lister, yield func(path string) error, ignoreError bool, join func(dir, name string) string) error {
	// MRI checks every start path up front and raises on the first missing one
	// before yielding anything.
	for _, r := range roots {
		if !lister.Exist(r) {
			return &MissingPathError{Path: r}
		}
	}

	for _, root := range roots {
		// ps is MRI's FIFO queue, seeded with one root; entries are shifted from
		// the front and a directory's sorted children are unshifted (reversed) onto
		// the front, yielding the depth-first ascending order.
		ps := []string{root}
		for len(ps) > 0 {
			file := ps[0]
			ps = ps[1:]

			// catch(:prune): yield the path, then (unless pruned or errored)
			// descend if it is a directory.
			err := yield(file)
			if err != nil {
				if err == ErrPrune {
					// Pruned: do not descend, continue with the next queued path.
					continue
				}
				return err
			}

			isDir, err := lister.IsDir(file)
			if err != nil {
				if ignoreError {
					continue
				}
				return err
			}
			if !isDir {
				continue
			}

			children, err := lister.Children(file)
			if err != nil {
				if ignoreError {
					continue
				}
				return err
			}
			sort.Strings(children)
			// reverse_each + unshift: push children onto the front in reverse so
			// that shifting visits them ascending.
			front := make([]string, 0, len(children)+len(ps))
			for _, c := range children {
				front = append(front, join(file, c))
			}
			ps = append(front, ps...)
		}
	}
	return nil
}
