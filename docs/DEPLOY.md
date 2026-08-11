# Release and Deploy

Forgejo is the canonical build and deployment system for ok-gobot. Production
deployments consume an exact immutable Forgejo Release; they do not build from a
mutable checkout and do not overwrite `/usr/local/bin` in place.

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

Run the `Deploy production` workflow from Forgejo Actions. The target host
identity stays in Forgejo variables rather than the public workflow. Supply
both inputs:

- `tag`: the exact Forgejo Release tag, such as `v0.4.0`;
- `confirmation`: `DEPLOY PRODUCTION <tag>` with the same exact tag.

The workflow requires a tag reachable from trusted `main`. It downloads the
Release through the Forgejo API, verifies its checksum and embedded version,
then streams only the extracted binary over a pinned SSH connection. Runtime
configuration, Telegram tokens, and AI credentials remain on the target host and are
never copied into the Actions runner.

The host-side forced command installs each binary into an immutable directory:

```text
<DEPLOY_ROOT>/releases/<tag>-<commit>/ok-gobot
<DEPLOY_ROOT>/current -> <DEPLOY_ROOT>/releases/<tag>-<commit>
```

It atomically switches `current`, restarts `ok-gobot.service`, and requires all
of these checks to pass:

- streamed binary SHA-256 equals the verified Release binary;
- embedded tag and commit match the selected Release;
- `ok-gobot doctor` succeeds with the existing runtime environment;
- service state is `active/running` and `NRestarts=0`;
- no error-priority journal entries appeared after restart.

After workflow success, send a Telegram message manually to verify the external
end-to-end path. This is intentionally not automated because Actions must not
receive runtime bot credentials.

Host provisioning and the required Forgejo variables/secrets are documented in
[`scripts/deploy/README.md`](../scripts/deploy/README.md). Provisioning is a
one-time operator action, not part of the deploy workflow.

## Redeploying an earlier release

There is no automatic rollback policy. If an operator deliberately needs an
earlier known-good build, run the same manual workflow with that exact Release
tag and matching confirmation. The installer reuses the already verified
immutable release directory and atomically switches `current` back to it.

## GitHub promotion mirror

Configure a native one-way Forgejo push mirror for
`https://github.com/BeFeast/ok-gobot`. Keep the GitHub repository writable so
Forgejo can update it, but leave GitHub workflows and Dependabot disabled. The
mirror is a public promotion/community surface, not another development forge.
