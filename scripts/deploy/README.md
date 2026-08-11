# Restricted production deploy command

The Forgejo deploy workflow downloads an exact Release tag, verifies its
checksum and embedded build metadata, then streams only the extracted binary to
the target host. Runtime configuration and Telegram/AI credentials never enter
the runner.

Install `ok-gobot-deploy-ssh` as `/usr/local/libexec/ok-gobot-deploy-ssh` and
`ok-gobot-install-release` as
`/usr/local/sbin/ok-gobot-install-release`, both owned by `root:root` and mode
`0755`. Give the dedicated deploy account only this sudo rule:

```sudoers
deploy-ok-gobot ALL=(root) NOPASSWD: /usr/local/sbin/ok-gobot-install-release *
```

Restrict the deploy public key in that account's `authorized_keys`:

```text
restrict,command="/usr/local/libexec/ok-gobot-deploy-ssh" ssh-ed25519 AAAA... forgejo-ok-gobot-deploy
```

Create `/etc/ok-gobot/deploy.conf` as `root:root`, mode `0640`. Values are
deployment-specific and deliberately do not live in this public repository:

```text
DEPLOY_ROOT=/srv/<application>
RELEASE_BASE_URL=https://forge.example.com/<owner>/<repository>/releases/download
SERVICE_UNIT=<systemd-unit>.service
RUNTIME_USER=<service-user>
RUNTIME_GROUP=<service-group>
RUNTIME_HOME=/home/<service-user>
RUNTIME_WORKDIR=/home/<service-user>/<workspace>
RUNTIME_ENV_FILE=/home/<service-user>/.config/<application>/runtime.env
RUNTIME_CODEX_HOME=/home/<service-user>/.config/<application>/codex
RUNTIME_PATH=/usr/local/bin:/usr/bin:/bin
```

Configure these Forgejo repository values:

| Kind | Name | Value |
| --- | --- | --- |
| variable | `PRODUCTION_DEPLOY_HOST` | SSH hostname or address visible to the Docker runner |
| variable | `PRODUCTION_DEPLOY_USER` | Dedicated restricted deploy account |
| variable | `PRODUCTION_DEPLOY_PORT` | SSH port; blank defaults to `22` |
| secret | `PRODUCTION_DEPLOY_SSH_KEY` | Private half of the forced-command deploy key |
| secret | `PRODUCTION_DEPLOY_KNOWN_HOSTS` | Pinned `known_hosts` line for the target host |

The installer accepts only `deploy <tag> <40-char-commit> <sha256>` through
`SSH_ORIGINAL_COMMAND`. It independently downloads the exact binary checksum
asset from the immutable HTTPS Release, verifies the streamed binary, creates
an immutable `<DEPLOY_ROOT>/releases/<tag>-<commit>` directory, atomically switches
`<DEPLOY_ROOT>/current`, restarts the configured service, and checks embedded
version, `doctor`, service state, restart count, and new journal errors.

Provision `DEPLOY_ROOT` as a canonical `root:root` directory with mode `0755`.
The installer rejects symlink components, writable deploy roots, concurrent
deployments, non-SemVer tags, and release bytes that do not match the
independently fetched binary checksum.

Repository secrets are safe only when every account with `Code: write` is
trusted to receive them. Branch protection alone is not a secret boundary:
a writer can add a workflow on another branch. Remove untrusted write access or
move deployment into a separately governed repository before adding the SSH key.

After the workflow succeeds, manually send a Telegram message to confirm the
end-to-end bot path. That check intentionally stays outside Actions so runtime
secrets are never copied into Forgejo.
