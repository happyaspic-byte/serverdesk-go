# Release and verification

## Current evidence and release boundary

2026-10-13 (user-local date; execution logs may use UTC 2026-10-12): the measured source is an
uncommitted working tree based on `3008e31`, not a new committed/released version. Local Go 1.26.9
race/shuffle, 83.1% coverage, JS 61/61, target builds/Windows test compilation, target vulnerability
scans, and rootless/mock deployment checks are recorded in [PRODUCTION-READINESS.md](PRODUCTION-READINESS.md).
No actual release run, archive-bound UAT, or Windows runtime validation was performed for these changes.
The procedure below describes the workflow contract, not approval already obtained.

## Maintainer procedure

1. Merge only a pull request with all required CI checks green.
2. Create an annotated, signed `vMAJOR.MINOR.PATCH` tag from the reviewed commit.
3. Push the tag. The `Release` workflow rebuilds and retests the commit.
4. A stable tag produces **candidate artifacts only**, not a GitHub Release. Retrieve the exact
   `sealed-candidate-RUN_ID-RUN_ATTEMPT` artifact from that successful run before UAT. It contains
   unchanged platform archives, SBOMs, `candidate-manifest.json`, `SHA256SUMS`, `provenance.jsonl`,
   and its separate `PROVENANCE_SHA256SUMS` (so attested `SHA256SUMS` is not changed after attestation).
   The manifest binds repository, commit, signed tag object, workflow ref, run/attempt, and file hashes;
   provenance attests those files and the manifest. Verify both before archive-bound UAT.
5. **Stable publication/promotion is BLOCKED.** Read-only preflight on 2026-10-12 found zero GitHub
   environments. No approval environment, reviewer protection, or promotion workflow was configured.
   A later separately authorized change must establish reviewed protection (independent reviewers,
   no self-review, bypass disabled, branch restrictions), fail-closed API rechecks, explicit
   archive-bound UAT evidence, exact artifact/provenance identity verification, and unchanged-asset
   promotion. An environment name or unsigned/self-asserted JSON is not approval. Do not rebuild/repack.
   Candidates expire after seven days; expired candidates require a new candidate and UAT.

Signed tags with an explicit suffix (for example `v1.2.3-rc.1`) publish only with `--prerelease`, after
candidate sealing and attestation. They are not stable acceptance or commercial certification.

The public workflow intentionally excludes Stratus MIBs, AVCLI, and a Java runtime. Those artifacts
must be obtained through an authorized customer/vendor channel and installed only under the applicable
terms. Do not attach an internally assembled bundle to a public GitHub Release.

The workflow uses commit-SHA-pinned Actions, `-trimpath`, disabled CGO, and an explicit target
matrix. It compiles each target binary once, then normalizes archive timestamps, ordering, ownership
and gzip/zip metadata, archives the same staged payload twice, and rejects byte differences.
This tests archive determinism, not independent compiler-build reproducibility. A payload contract also verifies binary names,
installer layout, notices and third-party licenses before publication. The contract also rejects
vendor MIB, AVCLI JAR, and bundled JRE files. Checksums detect transport/storage
corruption; provenance binds artifacts to this repository workflow and tag; the SBOM records the
shipped Go module and embedded assets.

Public archives deliberately exclude Stratus AVCLI, JRE distributions, and vendor MIB files. Obtain
and provision those through the customer's applicable vendor entitlement. The release workflow
rejects the staged payload if any of these artifacts appear.

## Customer verification

Download the archive and `SHA256SUMS` into the same directory. Stable candidates additionally
require the exact run's manifest and retained provenance; checksum matching alone is not trusted
approval or proof that UAT ran. Reject repository/commit/tag-object/workflow/run identity mismatches.
For candidate verification, provide `--bundle provenance.jsonl` to `gh attestation verify` instead
of relying on public-release lookup:

```bash
sha256sum --check SHA256SUMS --ignore-missing
# Retained candidate from the exact run (not public-release lookup):
gh attestation verify serverdesk-vX.Y.Z-linux-amd64.tar.gz \
  --repo happyaspic-byte/serverdesk-go --bundle provenance.jsonl
```

For an actually published prerelease, public attestation lookup may be used without `--bundle`.
Also verify `PROVENANCE_SHA256SUMS` for the retained provenance file and the manifest-bound identities;
these checks do not grant stable promotion approval.

On Windows, use `Get-FileHash -Algorithm SHA256` and compare the result with `SHA256SUMS`.
Reject an asset if the checksum, repository identity, tag, or attestation does not match.
