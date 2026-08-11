# Release and Deployment

`BeFeast/ok-gobot` is the canonical source, CI, and Release repository. Production
deployment authority lives in the separate public Forgejo repository
[`oleg/ok-gobot-deploy`](https://git.oklabs.uk/oleg/ok-gobot-deploy). Deployments
consume an exact immutable Forgejo Release; they do not build from a mutable
checkout or overwrite `/usr/local/bin` in place.

## Release lifecycle

1. Merge through the protected `main` branch after `CI / Security` passes.
2. Push a `v*` tag at the trusted `main` commit.
3. `.forgejo/workflows/release.yml` tests that commit and builds one
   CGO-enabled `linux_amd64` binary with the tag and commit embedded.
4. The workflow verifies the ELF architecture, shared libraries, SQLite startup
   smoke, archive checksum, and then publishes the archive and checksums to the
   Forgejo Release. Existing Releases are not overwritten.
5. A Forgejo native push mirror promotes trusted `main` and tags to GitHub.
   GitHub does not rebuild or publish a second copy.

Release artifacts follow this naming contract:

```text
ok-gobot_<tag>_linux_amd64.tar.gz
ok-gobot_<tag>_linux_amd64.tar.gz.sha256
ok-gobot_<tag>_linux_amd64.binary.sha256
checksums.txt
```

## Manual production deployment

Run the manual `Deploy production` workflow from
[`oleg/ok-gobot-deploy`](https://git.oklabs.uk/oleg/ok-gobot-deploy). That
repository owns the restricted host-side installer, provisioning contract,
Forgejo variables/secrets, checksum verification, and production acceptance
instructions. Runtime configuration, Telegram tokens, and AI credentials stay
on the target host and never enter either source repository.

## Redeploying an earlier release

There is no automatic rollback policy. If an operator deliberately needs an
earlier known-good build, follow the exact-tag redeploy procedure in
[`oleg/ok-gobot-deploy`](https://git.oklabs.uk/oleg/ok-gobot-deploy).

## GitHub promotion mirror

Configure a native one-way Forgejo push mirror for
`https://github.com/BeFeast/ok-gobot`. Keep the GitHub repository writable so
Forgejo can update it. The canonical tree intentionally contains no GitHub
workflows or Dependabot configuration, so the GitHub Actions repository setting
may remain enabled without creating a second CI or release path. The mirror is a
public promotion/community surface, not another development forge.
