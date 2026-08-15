# Contributing

Thanks for looking. This document says how to get a change in, and what a change has
to carry to be mergeable.

## Reporting things

| What | Where |
|---|---|
| A bug, a wrong number, a false positive | [open an issue](https://github.com/headroom-project/headroom/issues) |
| A vulnerability | privately, see [`SECURITY.md`](SECURITY.md). Please do not open a public issue |
| A ceiling this tool does not know yet | open an issue with the vendor documentation link that states it |

A wrong number is the most valuable bug report this project can receive. A capacity
report that is confidently wrong costs the person reading it more than no report at
all, so if a finding quotes a figure you can show is not right, that is a priority
issue and not a nitpick. Include the rule id, what it said, and the vendor page that
says otherwise.

Expect a first response on an issue within a week. Nobody is paid to be here, so that
is a good-faith target rather than an SLA.

## Getting set up

```bash
git clone https://github.com/headroom-project/headroom
cd headroom
go build -o bin/headroom ./cmd/headroom
go test ./...
```

Go only. There is no build system to learn, no code generation step, and no service to
stand up. If `go test ./...` passes you have a working checkout.

Everything needed to build is free software: the Go toolchain and one dependency,
`gopkg.in/yaml.v3`.

### On Windows with Application Control

Windows Application Control blocks unsigned binaries, which includes anything `go
build`, `go run` or `go test` produces. `run.ps1` cross-compiles for Linux and runs the
result through WSL, which is the only way to execute tests on such a machine. See the
README for the manual form.

## Sending a change

1. Branch from `main`. Name it for what it does: `fix/gp2-cliff`, `feat/rule-r009`.
2. Make the change, with tests.
3. Open a pull request. `main` is protected and does not take direct pushes.
4. CI has to be green. Every check is required, and none of them are advisory.

There is no CLA and no DCO sign-off. The Apache 2.0 license already covers the terms
under which a contribution is received.

## What a change has to carry

### Tests, without exception

**New behaviour arrives with a test, and a bug fix arrives with a test that fails
without the fix.** This is not a style preference here, it is the lesson the project
learned the expensive way: two ceilings shipped wrong through a fully green suite,
because the tests asserted that the code did what the catalog said and said nothing
about whether the catalog was true. One of them, MariaDB, had no test touching it at
all.

Assert silence as carefully as you assert findings. A rule that fires when it should
not is the failure mode that costs a user, so a change that makes a rule speak should
usually also add the case where it stays quiet.

### `gofmt` and `go vet`

Both are enforced in CI and neither is negotiable. Run `gofmt -w .` before pushing.

### The catalog is different from the code

`internal/catalog/data/` holds the ceiling tables, and it has a rule of its own:

**No entry ships without `source`, `verified_at` and `confidence`.**

- `source` is a deep link to the vendor page that states the figure, not a landing page,
  not a blog post, not Stack Overflow, and not memory.
- `confidence: high` only when the page states the figure directly. If you had to derive
  it, that is `medium`, and the derivation goes in `notes`.
- If a figure cannot be found in official documentation, **leave it out** and let the
  rule stay silent. The code is written to skip what the catalog does not know.

An absent ceiling always beats an uncertain one.

Changing a number in the catalog usually breaks a test, and that is working as
intended: those tests exist so a number cannot move without somebody noticing. If your
change moves one, say so plainly in the pull request description, at the top and not in
a footnote.

### Rules are not authorable in YAML, on purpose

If you are about to add a way for `headroom.yaml` to define a new built-in capacity
rule, please open an issue first. The value of a built-in rule is the curated ceiling
behind it, and a ceiling nobody verified is worse than no ceiling. YAML gets policy
(`gp2 is banned`, `subnets must be at least a /24`); capacity stays in code with a
source attached.

### The privacy boundary

`internal/extract/` reads by allowlist, never denylist. If your change adds a resource
type or an attribute, it goes on the allowlist explicitly and the pull request says why
that attribute is needed to compute a ceiling.

Anything that widens what leaves the machine is a security-relevant change even when it
looks routine, and `headroom analyze --dry-run` has to keep printing exactly what would
be uploaded. There is no second channel and there must never be one.

## Commit messages

Conventional prefixes: `feat:`, `fix:`, `test:`, `docs:`, `ci:`, `build:`, `chore:`,
with an optional scope like `feat(rules):`. The release notes are generated from these,
so the subject line is what a stranger reads to decide whether an upgrade matters to
them.

Say why, not what. The diff already says what.

## Style

Match the file you are in. Comments explain the reasoning that is not visible in the
code, especially the reasoning behind a threshold or a decision to stay silent. A
comment restating the line below it is noise; a comment explaining why the number is
9531392 is the reason somebody can maintain this later.
