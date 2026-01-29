# New Tools Documentation

This document demonstrates the usage of the newly implemented tools: `patch` and `grep`.

## PatchTool

The `patch` tool applies unified diff patches to files, allowing for precise file modifications.

### Usage

```
patch <filepath> <patch-content>
```

### Example

Given a file `example.txt`:
```
line 1
line 2
line 3
```

Apply a patch to add a new line:
```
patch example.txt "--- a/example.txt
+++ b/example.txt
@@ -1,3 +1,4 @@
 line 1
+new line
 line 2
 line 3"
```

Result:
```
line 1
new line
line 2
line 3
```

### Features

- Parses unified diff format (standard patch format)
- Supports multiple hunks in a single patch
- Validates context lines to ensure patch applies correctly
- Respects BasePath security restriction
- Returns descriptive error messages for invalid patches

### Security

The tool enforces path restrictions - files must be within the configured BasePath to prevent unauthorized file access.

## SearchFileTool (grep)

The `grep` tool searches for text patterns in files recursively, similar to the Unix grep command.

### Usage

```
grep <pattern> [directory]
```

### Examples

Search for "TODO" in all files:
```
grep "TODO"
```

Search in a specific directory:
```
grep "function" src/
```

Use regex patterns:
```
grep "func.*Test"
```

### Features

- Supports regular expressions
- Recursive file search
- Returns matches in `file:line: content` format
- Limits to 50 matches for performance
- Skips binary files and common ignore directories:
  - `.git`
  - `node_modules`
  - `.idea`
  - `__pycache__`
  - `.venv`
  - `vendor`
- Recognizes common text file extensions:
  - Source code: `.go`, `.py`, `.js`, `.ts`, `.java`, `.c`, `.cpp`, `.rs`, etc.
  - Config files: `.json`, `.yaml`, `.toml`, `.xml`, `.env`, `.ini`, etc.
  - Documentation: `.md`, `.txt`, `.log`, etc.
  - Special files: `Makefile`, `Dockerfile`, `README`, `LICENSE`, etc.

### Output Format

```
Found N matches:
path/to/file.go:15: func TestExample() {
path/to/other.go:42: // Example comment
```

### Security

Like the PatchTool, the grep tool respects BasePath restrictions to prevent searching outside allowed directories.

## Integration

Both tools are automatically registered when calling `LoadFromConfig()` or `LoadFromConfigWithOptions()` with a non-empty basePath:

```go
registry, err := tools.LoadFromConfig("/path/to/base")
// Tools are now available:
// - registry.Get("patch")
// - registry.Get("grep")
```

## Testing

Comprehensive tests are provided in:
- `patch_test.go` - Tests for patch functionality
- `search_file_test.go` - Tests for file search functionality
- `registry_test.go` - Integration tests for tool registration

Run tests with:
```bash
go test ./internal/tools -v
```
