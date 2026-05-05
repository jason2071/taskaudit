# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

`taskaudit` — CLI tool that scans a Go project directory and calls Claude API to verify which checklist items are implemented in the code. Outputs terminal report, HTML, or Markdown.

## Build & Run

```bash
go build -o taskaudit
./taskaudit -task "Feature name" -dir ./target-project
```

No external dependencies — stdlib only (`go.mod` has no `require`).

## Architecture

Single-file CLI (`main.go`, ~970 lines). Flow:

1. `parseFlags()` → config
2. `loadChecklist()` → default 9-item Go backend checklist or custom file (`category: title` format)
3. `scanFiles()` → walks dir tree, collects `.go` files from include paths (default: `internal/handler,service,repository,models,middleware`), skips vendor/node_modules/.git, enforces 100KB per-file limit
4. `auditCode()` → builds prompt with task context + checklist + code content, calls Claude Messages API, parses JSON response
5. Output: terminal (`printReport`), JSON (`-json`), HTML (`-html`), Markdown (`-md`)

Key types: `config`, `codeFile`, `checklistItem`, `auditResult`, `apiRequest/Response`.

## Environment

- `ANTHROPIC_API_KEY` required
- Uses model `claude-sonnet-4-20250514`, 4000 max tokens, 120s timeout

## Flags

`-task` (required), `-desc`, `-dir`, `-checklist`, `-include`, `-tests`, `-json`, `-html`, `-md`, `-open`, `-v`
