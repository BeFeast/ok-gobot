---
name: add-knowledge
description: Save incoming knowledge to the configured Obsidian vault using the existing PARA-style domain layout.
---

# Add Knowledge

Use this workflow when the user asks to save a discovery, article, tool, or other knowledge in Obsidian.

1. Work only through the configured `obsidian` tool. If it is unavailable, report that `obsidian.vault_dir` or `OKGOBOT_OBSIDIAN_VAULT_DIR` must be configured.
2. Choose the best existing domain. Common routes are `AI/Resources/Topics/`, `Business/Resources/Topics/`, `Dev/Resources/Topics/`, `Finance/Resources/Topics/`, `Health/Resources/Topics/`, `HomeLab/Resources/Topics/`, `Life/Resources/Topics/`, `Music/Resources/Topics/`, `Video/Resources/Topics/`, and `Work/Resources/Topics/`.
3. Use `obsidian search` and `obsidian list` before writing. Update a clear existing match instead of creating a duplicate.
4. New filenames use `kebab-case.md`. Include frontmatter with `title`, `type: digest-topic`, `updated`, `area`, `tags`, and source links when known.
5. Structure the note as `Overview`, `Key Facts`, `Links / Sources`, and `Status`; add `Comparison` only when useful.
6. Preserve prior content and source links on update. Never store credentials or raw secrets.
7. Read the final note back and report its vault-relative path.
