# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout

`github.com/womat/manchester` is Manchester line coding for Raspberry Pi services, split out of
`github.com/womat/golib` (where it lived as `golib/manchester`) with its history. The copy in
golib is deprecated and removed there in golib v2; the only consumer, `tadl`, moves over on its
own schedule.

One module, two packages: `encoder` and `decoder`. They are versioned together on purpose —
sender and receiver must agree on convention, bit order and sync bytes, and
`roundtrip_test.go` in the root tests both ends against each other. `manchester.go` in the root
holds only the package doc comment, so pkg.go.dev has a landing page.

Everything under `demo/` is a **separate Go module** with its own `go.mod` and a
`replace github.com/womat/manchester => ../..`. The demos use `github.com/womat/gpio` for the
line, so they only build for Linux.

## Commands

```sh
go test -race ./...                          # the module builds and tests on macOS too
go test ./decoder/ -run TestRecovery -v      # one test
scripts/check.sh                             # go fix, vet, golangci-lint, govulncheck, all modules
scripts/check.sh --check --skip=vuln         # report only, no network
```

`scripts/check.sh` defaults to `GOOS=linux`: `encoder/sleep_linux.go` is the file that ships,
and the demos do not typecheck on macOS because of `gpio/rpi`.

Demos: `cd demo/<name>` first, then use its `Makefile`; `make deploy` scp's the binary to
`$(PI_USER)@$(PI_HOST)`.

There is one branch, `main`; a version is an annotated tag `vX.Y.Z` on it.

**This repository always authenticates as `womat`.** The remote goes through the
`github-priv` host alias from `~/.ssh/config`; a fresh clone needs
`git remote set-url origin git@github-priv:womat/manchester.git`.

## Architecture

`encoder` and `decoder` are deliberately decoupled from any transport: the encoder takes a
`SetValue func(Level) error` callback, and the decoder consumes a channel of its own
`decoder.Event` values. The demos do the glue — a goroutine translating `gpio.Event` into
`decoder.Event` (see `demo/manchester_listener/cmd/main.go`). Keep that boundary: don't import
`gpio` from `encoder` or `decoder`, and don't let either import the other.

The decoder works in two phases — clock recovery (finds the bit period in the edge timings and
follows it, `decoder/recovery.go`; skipped when a bit clock is configured) followed by bit
decoding with timing tolerance — and runs asynchronously; bits arrive on `Bits()`, and `Close()`
performs the ordered shutdown. `decoder.Event.Missed` mirrors `gpio.Event.Missed`, so an interval
spanning lost edges is not taken for a bit period. Both packages support IEEE 802.3 and
Differential Manchester (Thomas) encodings, selected via `With...` options.

The encoder sends asynchronously from its own goroutine; `Wait()` blocks until a transmission is
done. On Linux it lowers the thread's timer slack (`sleep_linux.go`, `golang.org/x/sys/unix`) —
the module's only dependency outside the standard library. The encoder has no logger and reports
transmission errors through `WithErrorHandler` (unset, they are dropped); the decoder takes a
`*slog.Logger` through `WithLogger`.

## Conventions

- Each package has a doc comment with a runnable `# Example usage` section in its main file,
  and `README.md` covers both; keep them updated when the API changes. The README ends with a
  *Not addressed* section — decisions against a fix go there.
- Sentinel errors are exported vars compared with `errors.Is`. `encoder`'s messages carry no
  package prefix. Don't change an existing message — services consume this module.
- `Encoder` and `Decoder` have an explicit `Close()` that callers must defer.
- Commit subjects follow Conventional Commits, `type(scope): description`, types `feat`,
  `fix`, `docu`, `chore`, `refactor`.

## Known state

Linter configuration is `.golangci.yml`; the demo modules find it by walking up. `errcheck`
ignores the deferred `Close` of `Encoder`, `Decoder` and `gpio.Pin`, and test files. **Do not
widen these exclusions to make a new finding go away; fix the finding or record the decision.**

**CI:** `.github/workflows/ci.yml`, on pushes to `main` and pull requests, runs on Linux.
Actions are pinned to a commit SHA with the release in a comment; dependabot updates the pins
and the Go dependencies of all modules in one group. Jobs: `format`, `checks`
(`scripts/check.sh --check`), `library` (vet plus `go test -race -cover`), `cross` (five Linux
targets plus Windows and macOS — the codec is portable), `demos`. All modules declare
`go 1.27`, the same as golib and its consumers; keep them in step.
