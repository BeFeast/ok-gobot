# Reliability Benchmark Harness

The reliability benchmark checks the autonomous PR lifecycle with a small,
manifest-driven harness. The default manifest is deterministic and uses fake
scenarios, so it runs locally without GitHub credentials, Telegram credentials,
or live LLM calls.

Run the default fake scenario suite:

```bash
ok-gobot benchmark reliability
```

From a checkout, this also works without installing the binary:

```bash
go run ./cmd/ok-gobot benchmark reliability
```

The compact report prints pass, fail, and skip counts plus failure buckets:

```text
Reliability benchmark: autonomous-pr-lifecycle-fakes (7 scenarios)
PASS 1  FAIL 5  SKIP 1
Categories: agent_failure=2  environment_failure=1  ci_failure=1  review_failure=1  policy_gated_skip=1
```

Write machine-readable JSON and human-readable Markdown artifacts:

```bash
ok-gobot benchmark reliability \
  --json-out reliability-report.json \
  --markdown-out reliability-report.md
```

Use `--format json` or `--format markdown` to print either artifact to stdout.
Use `--fail-on-failure` when wiring the benchmark into a gate that should exit
non-zero if any scenario is blocked.

## GitHub-Backed Provider

The `github` provider scores recorded Maestro sessions against read-only GitHub
PR state. It requires an existing ok-gobot SQLite storage file with evidence
ledger entries and authenticated `gh` access.

Score recent sessions with evidence:

```bash
ok-gobot benchmark reliability \
  --provider github \
  --repo BeFeast/ok-gobot \
  --lookback 20
```

Score explicit sessions:

```bash
ok-gobot benchmark reliability \
  --provider github \
  --repo BeFeast/ok-gobot \
  --session agent:default:telegram:dm:123 \
  --session agent:default:telegram:dm:456
```

The provider uses local evidence to correlate the session, issue, branch, PR,
retry count, and evidence ledger. It uses GitHub only for read operations such
as PR state, checks, and reviews. It never writes issues, PRs, labels, comments,
checks, branches, or worktrees.

GitHub-backed JSON and Markdown reports include a `data_source` plus evidence
links for the issue, PR, checks, reviews, and session evidence ledger when those
links are available. Missing GitHub authentication or missing explicit session
state fails before scoring; run `gh auth login` or choose a session from
`ok-gobot sessions list` with evidence visible via `ok-gobot sessions evidence`.

## Manifest

The default fake manifest lives at
`benchmarks/reliability/fake-scenarios.yaml`. Each scenario records lifecycle
events for these states:

- `issue_selected`
- `preflight_passed`
- `branch_created`
- `pr_opened`
- `ci_checked`
- `review_checked`
- `retry_attempted`
- `merge_ready_emitted`
- `blocker_emitted`

Terminal outcomes are `merge_ready`, `blocked`, or `skipped`. Blocked and
skipped scenarios include one failure category: `agent_failure`,
`environment_failure`, `ci_failure`, `review_failure`, or `policy_gated_skip`.

The runner is provider-neutral. The bundled `fake` provider replays manifest
events for local testing. The `github` provider implements the same
`reliability.Evaluator` interface and reuses the existing report generation.
