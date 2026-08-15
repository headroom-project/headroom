# Security

## Reporting a vulnerability

Report privately through GitHub, at
[Security advisories](https://github.com/headroom-project/headroom/security/advisories/new).
That opens a channel only the maintainers can read.

Please do not open a public issue for a vulnerability.

Expect a first response within 3 working days, and an assessment within 10. If a
fix is warranted, the advisory is published together with the release that
carries it, with credit unless you ask otherwise.

## What is in scope

The CLI, the extraction allowlist, the redaction path, and the release pipeline.

The single thing worth attacking here is the boundary between a Terraform plan
file and anything that leaves the machine, so anything that widens that boundary
is the highest-value report you can send:

- an attribute reaching the payload that is not on the allowlist
- a resource address surviving into the payload unhashed
- finding prose, which is rebuilt server side and never uploaded, appearing in
  the payload
- `--dry-run` printing something different from what an upload would send

That last one is the load-bearing claim. `--dry-run` is the only channel through
which a user can audit what would travel, so a divergence between it and the real
upload is a vulnerability even if nothing sensitive escapes in the specific case.

## What is not in scope

- A Terraform plan file that already contains a secret. Plan files can hold
  sensitive values, which is why this tool reads by allowlist. The plan itself is
  the user's to protect.
- Findings that are numerically wrong. Those are bugs, and serious ones, but they
  go in the public issue tracker.
- Denial of service by feeding the CLI a deliberately enormous plan.

## How this repository is scanned

Every push, every pull request, and once a week on a schedule:

| Check | What it is for |
|---|---|
| `govulncheck` | Go advisories, at symbol level: it reports a CVE only when a code path actually reaches the vulnerable function, so a graph match with no reachable call does not become noise |
| CodeQL, `security-extended` | static analysis of this repository's own Go code |
| OpenSSF Scorecard | supply-chain posture of the repository itself, published so it can be checked by somebody who does not work here |
| Dependency review | blocks a pull request that would introduce a dependency with a known advisory |
| Dependabot | Go modules and GitHub Actions, weekly |

Results land in the repository's **Security** tab. `govulncheck` also runs again
inside the release workflow, before anything is published, so a release cannot
ship past an advisory that CI would have caught.

## Dependencies

One, `gopkg.in/yaml.v3`, which has no dependencies of its own. Everything else is
the Go standard library.

That is a deliberate constraint rather than an accident of scope. This binary runs
inside customer environments, and every transitive dependency is supply-chain
surface that somebody has to defend during a security review.

## Verifying a release

Every release ships `checksums.txt`, a Sigstore signature over it, and an SPDX
SBOM per archive.

```bash
# 1. the archive matches the published checksum
sha256sum -c checksums.txt --ignore-missing

# 2. the checksum file itself came from this repository's release workflow
cosign verify-blob \
  --bundle checksums.txt.bundle \
  --certificate-identity-regexp 'https://github.com/headroom-project/headroom/.github/workflows/release.yml@refs/tags/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 3. the binary was built by that workflow, from that commit
gh attestation verify headroom_linux_amd64.tar.gz --repo headroom-project/headroom
```

The signing is keyless. There is no private key held by a person, so there is no
private key for a person to lose: the certificate binds the signature to the
workflow identity, and step 2 is what checks that binding.
