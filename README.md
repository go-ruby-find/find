<p align="center"><img src="https://raw.githubusercontent.com/go-ruby-find/brand/main/social/go-ruby-find-find.png" alt="go-ruby-find/find" width="720"></p>

# find — go-ruby-find

[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--coverage)

**A pure-Go (no cgo) reimplementation of the traversal algorithm of Ruby's
[`Find`](https://docs.ruby-lang.org/en/master/Find.html) module
(`require "find"`)** — the deterministic, interpreter-independent core of MRI
4.0.5's `lib/find.rb`. It drives `Find.find`'s exact top-down visit order and the
`Find.prune` control flow over an **injected directory lister**, so the real
filesystem access (`Dir.children`, `File.lstat`/`File.directory?`) stays where it
belongs — host-side — while the order, the sort, the prune throw/catch and the
error pass-through behaviour live here as portable Go.

It is the `Find` backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime — a sibling
of [go-ruby-regexp](https://github.com/go-ruby-regexp/regexp) (the Onigmo engine),
[go-ruby-erb](https://github.com/go-ruby-erb/erb) (the ERB compiler) and
[go-ruby-yaml](https://github.com/go-ruby-yaml/yaml) (the Psych port).

> **What it is — and isn't.** Reproducing `Find.find`'s *order* — the depth-first
> walk with byte-wise-sorted children, the `prune` semantics, the missing-start-path
> error — is fully deterministic and needs **no interpreter**, so it lives here as
> pure Go. *Touching the filesystem* — opening a directory, stat-ing a path — is the
> host's job; this library asks for it through a small [`Lister`](#the-lister-seam)
> interface that the host (rbgo) binds to `Dir.children` and `File.lstat`.

## MRI faithfulness

Validated against the `ruby` binary on every non-Windows CI lane (the
`*_oracle*` differential tests build a real temp tree and diff our order against
`ruby -rfind`). The algorithm mirrors MRI 4.0.5's `lib/find.rb` exactly:

- **Order.** Each start path is yielded first, then a depth-first walk of its
  contents. A directory's children are listed, **sorted ascending byte-wise**
  (so `Capital.txt` precedes `a`, and `9` precedes letters — MRI's `String#<=>`),
  reversed, and unshifted onto a FIFO queue, giving depth-first ascending order.
- **`Find.prune`.** Returning `find.ErrPrune` from the yield callback prunes the
  current path: it has already been yielded, but if it is a directory it is not
  descended into — the engine's analogue of `throw :prune`.
- **Errors.** A missing **start** path makes `Walk` return a `*MissingPathError`
  before anything is yielded (MRI raises `Errno::ENOENT`). A per-entry `IsDir`/
  `Children` failure reached mid-walk is **swallowed** (entry skipped) when
  `ignoreError` is true — MRI's default — and **propagated** otherwise.

## The `Lister` seam

`Walk` performs no I/O itself; the host injects all filesystem access:

```go
type Lister interface {
    Exist(path string) bool                       // Ruby File.exist?  (start paths only)
    IsDir(path string) (isDir bool, err error)    // Ruby File.lstat(path).directory?
    Children(dir string) (entries []string, err error) // Ruby Dir.children (base names, unsorted ok)
}

func Walk(roots []string, l Lister, yield func(path string) error, ignoreError bool) error
```

`rbgo` supplies a `Lister` backed by its own `Dir`/`File` objects; the engine
sorts the children itself, so the host need not. `WalkJoin` accepts a custom path
joiner for hosts whose `File.join` differs from the default single-`/` rule.

```go
err := find.Walk([]string{"a", "b"}, hostLister, func(p string) error {
    if shouldSkipSubtree(p) {
        return find.ErrPrune // == Find.prune
    }
    fmt.Println(p)
    return nil
}, true)
```

## Tests & coverage

`go test ./...` — **100% statement coverage**, enforced in CI. The coverage is
reached entirely by the **deterministic, ruby-free** tests (an in-memory `Lister`,
no real filesystem), so the Windows and qemu lanes pass the gate without a Ruby
runtime; the MRI oracle runs additionally on the ubuntu/macos lanes. Builds and
tests on all six 64-bit Go targets (amd64, arm64, riscv64, loong64, ppc64le,
s390x) across Linux, macOS and Windows. CGO is not used.

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright (c) 2026, the go-ruby-find/find
authors.
