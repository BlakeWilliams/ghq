# Best Practices

## UI Rendering

- **DO** use `lipgloss.Width()` to measure visible string width. It correctly ignores ANSI escape codes.
- **DON'T** use `len()`, `utf8.RuneCountInString()`, or similar to measure display width of rendered strings — they count escape codes and give wrong results.

- **DO** derive all colors from the terminal's ANSI palette. Use named ANSI colors (`lipgloss.Cyan`, `lipgloss.Green`, `lipgloss.BrightBlack`, etc.) so the UI adapts to the user's terminal colorscheme.
- **DON'T** hardcode hex color values (e.g. `lipgloss.Color("#ff0000")`). The only exception is GitHub label colors from the API, which are inherently fixed.
- **BEWARE** `lipgloss.Width("\t")` returns 0, but terminals render tabs as 8 columns. Expand tabs to spaces before measuring or padding strings.

## Bubble Tea Patterns

- **DO** return a `tea.Cmd` for all async/IO work. Never block inside `Update()`.
- **DO** use surgical render list operations (`SpliceThreadForComment`, `InsertThread`, `RemoveThread`, `ReplaceThread`) for incremental updates instead of calling `FormatFile` to re-render the entire file.

## Architecture

- **DO** use the `Renderable` / `FileRenderList` infrastructure (`internal/ui/components/render_list.go`) to represent visual elements in the diff view. Each element implements `Renderable` with `Render()`, `RenderedLineCount()`, and `DiffIdx()`. The render list handles layout, offset calculation, and caching automatically.
- **DON'T** post-process rendered strings to inject UI elements (e.g. string-splitting rendered output to splice in a textarea). This breaks when line counts change and produces fragile offset math. Instead, model the element as a `Renderable` and insert it into the `FileRenderList`.

  ```
  // Bad: string surgery after rendering
  lines := strings.Split(rendered, "\n")
  lines = append(lines[:insertAt+1], inputLines...)

  // Good: structural insertion via render list
  inputItem := NewCommentInputItem(diffIdx, side, line, textarea)
  list.InsertAfterDiffLine(diffIdx, inputItem)
  ```

- **DO** model distinct UI concepts as types. If you find yourself tracking related state across multiple parallel maps or recombining loose fields, introduce a struct that captures the concept.

  ```
  // Bad: parallel maps that must stay in sync
  CopilotReplyBuf  map[string]string
  CopilotPending   map[string]bool
  CopilotIntent    map[string]string

  // Good: a struct that owns the concept
  type CopilotPendingInfo struct {
      Path   string
      Line   int
      Side   string
  }
  CopilotPending map[string]CopilotPendingInfo
  ```

## Testing

- **DO** use `assert` and `require` from `github.com/stretchr/testify` for all test assertions.
- **DON'T** use `t.Errorf`, `t.Fatalf`, `t.Error`, `t.Fatal`, or manual `if`-then-fail patterns. Use `assert.*` (continues on failure) or `require.*` (stops on failure) instead.

  ```
  // Bad
  if got != want {
      t.Errorf("got %d, want %d", got, want)
  }

  // Good
  assert.Equal(t, want, got)
  require.NoError(t, err)
  ```

## ANSI String Manipulation

- **BEWARE** every `\033[0m` reset kills the current background color. When composing styled content with nested ANSI sequences (e.g. tool group rows inside comment thread borders), re-inject the background escape after each reset.
- **DO** persist and restore cursor position per-file across mode switches using `SaveViewState`/`LoadViewState`. New views that track file-level cursor state should follow this pattern.
