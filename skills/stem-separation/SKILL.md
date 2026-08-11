---
name: stem-separation
description: Separate vocals and instrumental stems from a lawful local media file through a deployment-configured wrapper or service.
---

# Stem Separation

Use this for vocal removal, instrumental creation, acapella extraction, or stem splitting from a local file.

## Safety and configuration gate

- Accept a local media file only. A streaming-service URL is reference metadata, not a processable input; ask for a lawful local copy.
- Do not install Demucs, Python, Node, models, drivers, or package managers on demand.
- The deployment must define a fixed wrapper or service invocation in `TOOLS.md`, for example a `stem_separation` entry with its command, supported options, output root, and expected artifact names. Do not assume an internal hostname or absolute wrapper path.
- Any wrapper invoked through `local` requires the fail-closed Telegram approval callback. If approval is not wired, or the command is denied, stop without falling back to direct shell execution.

## Workflow

1. Validate that the input is an existing regular media file under an allowed filesystem root. Reject shell metacharacters or ambiguous paths rather than constructing an unsafe command.
2. Read the configured wrapper/service contract from `TOOLS.md`. Report a configuration gap if it is absent.
3. Use the configured inspection command when duration or format matters. Do not invoke arbitrary binaries discovered on `PATH`.
4. Invoke the fixed wrapper with the input as a quoted argument and only documented options. The operator must approve the `local` call.
5. Require separate instrumental and vocals artifacts. Keep lossless output when the user wants DAW work; use a compressed preview only when requested.
6. Verify that output files exist, are non-empty, and remain inside the configured artifact root. Report paths and the backend/model named by the wrapper.

For a YouTube URL that should run the full hosted pipeline, use the native `/youtube_karaoke` workflow instead of this local-file wrapper.
