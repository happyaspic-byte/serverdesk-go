# Release and verification

## Maintainer procedure

1. Merge only a pull request with all required CI checks green.
2. Create an annotated, signed `vMAJOR.MINOR.PATCH` tag from the reviewed commit.
3. Push the tag. The `Release` workflow rebuilds and retests the commit.
4. Confirm that the GitHub Release contains both platform archives, CycloneDX SBOMs,
   `SHA256SUMS`, and GitHub build-provenance attestations.
5. Promote the exact release assets to the customer delivery channel. Do not rebuild locally.

The public workflow intentionally excludes Stratus MIBs, AVCLI, and a Java runtime. Those artifacts
must be obtained through an authorized customer/vendor channel and installed only under the applicable
terms. Do not attach an internally assembled bundle to a public GitHub Release.

The workflow uses commit-SHA-pinned Actions, `-trimpath`, disabled CGO, and an explicit target
matrix. It normalizes archive timestamps, ordering, ownership and gzip/zip metadata, builds every
archive twice, and rejects byte differences. A payload contract also verifies binary names,
installer layout, notices and third-party licenses before publication. The contract also rejects
vendor MIB, AVCLI JAR, and bundled JRE files. Checksums detect transport/storage
corruption; provenance binds artifacts to this repository workflow and tag; the SBOM records the
shipped Go module and embedded assets.

Public archives deliberately exclude Stratus AVCLI, JRE distributions, and vendor MIB files. Obtain
and provision those through the customer's applicable vendor entitlement. The release workflow
rejects the staged payload if any of these artifacts appear.

## Customer verification

Download the archive and `SHA256SUMS` into the same directory:

```bash
sha256sum --check SHA256SUMS --ignore-missing
gh attestation verify serverdesk-vX.Y.Z-linux-amd64.tar.gz \
  --repo happyaspic-byte/serverdesk-go
```

On Windows, use `Get-FileHash -Algorithm SHA256` and compare the result with `SHA256SUMS`.
Reject an asset if the checksum, repository identity, tag, or attestation does not match.
