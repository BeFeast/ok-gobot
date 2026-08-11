---
name: digest-curator
description: Build or improve an Obsidian daily digest and merge recurring insights into routed topic notes.
---

# Digest Curator

This is a review and curation workflow. Deterministic scheduled aggregation remains an external timer, not an agent job.

1. Work through the configured `obsidian` tool. Daily notes are under `_Assets/Daily Notes/YYYY/MM/DD/` relative to the vault root.
2. List the requested date directory. Include summary Markdown files and exclude `transcripts/`, `_index.md`, and an existing `YYYY-MM-DD-daily-digest.md`.
3. Read the routing source `AI/Resources/pipelines/digest-routing-config.yaml` when it exists. Do not invent a new top-level taxonomy or bypass its forbidden domains.
4. Read the source summaries and cluster them by actual recurring topics. Do not use single keyword frequency as a theme extractor.
5. Write `YYYY-MM-DD-daily-digest.md` with frontmatter and the sections `Main themes`, `Top insights`, `Action items worth keeping`, and `Source summaries`.
6. For each recurring topic, search for an existing routed topic note. Merge insights and source wikilinks idempotently; create a narrowly scoped note only when no match exists.
7. Verify the digest and updated topic notes by reading them back. Report the source count and every created or updated vault-relative path.

Do not create empty digests. Do not write one topic note per source video. Never place raw credentials in notes.
