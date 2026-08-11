---
name: transcript-summary
description: Summarize an existing Obsidian transcript in Russian, save the summary beside it, and cross-link both notes.
---

# Transcript Summary

1. Resolve the user-provided path relative to the configured Obsidian vault. Append `.md` only when the path has no extension; ask for a fuller path only when the note is genuinely ambiguous.
2. Read the complete transcript through the `obsidian` tool and preserve its body exactly.
3. Generate a Russian summary containing:
   - `Основная идея`: 3–5 sentences;
   - `Ключевые моменты`: 10–15 concrete bullets with bold topic labels, names, tools, versions, and numbers where present;
   - `Выводы / Action Items` when there are practical actions;
   - 3–7 lowercase content tags.
4. Save it beside the transcript as `<original stem> — Summary.md` with frontmatter: `type: summary`, `date`, `source` wikilink, `language: ru`, and `tags`.
5. Update only the original transcript's frontmatter to include `type: transcript`, `date`, detected `language`, and a `summary` wikilink. Preserve all other useful frontmatter keys and the complete transcript body.
6. Read both notes back. Report their vault-relative paths, detected source language, tag list, and number of key points.

Do not fabricate details absent from the transcript and do not expose absolute host paths in the notes or reply.
