# gg

Interactive local diffs. Review, stage, and polish your changes with agents. Think `git add -p`, but with super powers.

![demo](assets/demo.gif)

- Interactive staging with a live diff view
- Ask Copilot questions about your code as you review
- Make finishing touches to your changes inline
- Respond to and interact with PR review comments

## Install

```
go install github.com/blakewilliams/gg/cmd/gg@latest
```

Requires Go 1.25+ and a GitHub token (via the `gh` CLI or `GITHUB_TOKEN` env var).

## Usage

Just run `gg` in a branch with changes.

**Navigation**

- `j` / `k` — move up/down
- `ctrl+j` / `ctrl+k` — next/previous file
- `ctrl+p` — fuzzy file picker
- `gg` / `G` — top/bottom
- `ctrl+d` / `ctrl+u` — half-page scroll

**Staging**

- `s` — stage line/selection (working tree view)
- `S` — stage hunk
- `u` / `U` — unstage line or hunk (staged view)
- `m` — cycle view: working → staged → branch

**Search**

- `/` — open search, type query, press `enter` to confirm
- `n` / `N` — next/previous match
- `esc` — clear search

**Actions**

- `enter` — ask Copilot about current line
- `r` — reply to comment thread
- `x` — resolve/unresolve thread
- `:` — command palette

Press `?` for the full list of keybindings.

testing, please ignore
