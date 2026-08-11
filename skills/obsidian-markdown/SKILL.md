---
name: obsidian-markdown
description: Create and edit Obsidian Flavored Markdown with properties, wikilinks, embeds, callouts, and tags.
---

# Obsidian Flavored Markdown

Use standard Markdown for structure and these Obsidian extensions where they add value:

- Internal notes: `[[Note Name]]`, `[[Note Name|Label]]`, `[[Note#Heading]]`, and `[[Note#^block-id]]`.
- Embeds: `![[Note]]`, `![[image.png|300]]`, and `![[document.pdf#page=3]]`.
- Callouts: `> [!note]`, `> [!warning] Title`, and foldable `> [!faq]-`.
- Properties: YAML frontmatter with stable keys such as `title`, `date`, `tags`, `aliases`, and `status`.
- Tags: lowercase `#tag` or `#nested/tag`; avoid near-duplicate spellings.
- Hidden comments: `%%hidden text%%`; highlights: `==highlighted text==`.

Use wikilinks for vault-internal references and normal Markdown links for external URLs. Preserve existing frontmatter keys and note conventions unless the user explicitly asks for a migration. After writing through the `obsidian` tool, read the note back and check that YAML fences, callouts, links, and code fences are balanced.
