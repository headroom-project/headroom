<!--
Thanks for the change. The checklist below is short on purpose: it is the set of
things that have actually gone wrong in this repository before.
-->

## What this changes, and why

<!-- Say why. The diff already says what. -->

## Checklist

- [ ] Tests added. New behaviour arrives with a test; a fix arrives with a test that
      fails without it.
- [ ] `gofmt -w .` and `go vet ./...` are clean.
- [ ] Silence is covered too, where relevant: a change that makes a rule speak usually
      also needs the case where it stays quiet.

## Does this move a number in the catalog?

- [ ] No.
- [ ] Yes, and it is described below with the vendor deep link that states the new
      figure, and the `confidence` it now carries.

<!--
If yes, put it at the top of this description rather than in a footnote. Two ceilings
once shipped wrong through a fully green suite, so a moved number is the headline of a
pull request, not a detail.
-->

## Does this change what leaves the machine?

- [ ] No. No new resource type, no new attribute on an allowlist in `internal/extract/`.
- [ ] Yes, and the description says which attribute, and why that attribute is needed
      to compute a ceiling.

<!--
Anything widening the payload is a security-relevant change even when it looks routine.
`headroom analyze --dry-run` has to keep printing exactly what would be uploaded.
-->
