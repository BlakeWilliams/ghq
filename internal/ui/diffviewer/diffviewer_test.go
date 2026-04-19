package diffviewer

import (
	"regexp"
	"strings"
	"testing"

	"github.com/blakewilliams/gg/internal/ui/components"
)

func TestByteToVisual(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		byteOff int
		want    int
	}{
		{"no tabs", "func hello", 5, 5},
		{"leading tab", "\thello", 1, 4},          // tab = 4 spaces
		{"after tab", "\thello", 2, 5},             // 4 + 1
		{"two tabs", "\t\thello", 2, 8},            // 4 + 4
		{"tab then match", "\tfunc hello", 6, 9},   // 4 + "func " = 9
		{"zero offset", "anything", 0, 0},
		{"end of string", "abc", 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := components.ByteToVisual(tt.s, tt.byteOff)
			if got != tt.want {
				t.Errorf("ByteToVisual(%q, %d) = %d, want %d", tt.s, tt.byteOff, got, tt.want)
			}
		})
	}
}

func TestHighlightSearchSpans_PlainText(t *testing.T) {
	gutter := "   1    2 +"
	code := "func hello() {"
	inner := gutter + code + strings.Repeat(" ", 20)
	raw := "func hello() {"

	pattern := regexp.MustCompile("(?i)hello")
	bgCode := "\033[43m"
	resetCode := "\033[0m"
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, bgCode, "", resetCode)

	if !strings.Contains(result, bgCode+"hello"+resetCode) {
		t.Errorf("expected yellow bg around 'hello', got: %q", result)
	}

	if !strings.HasPrefix(result, gutter) {
		t.Errorf("gutter should be unchanged, got prefix: %q", result[:len(gutter)+10])
	}
}

func TestHighlightSearchSpans_WithTabs(t *testing.T) {
	gutter := "   1    2 +"
	code := "    func hello() {"
	inner := gutter + code + strings.Repeat(" ", 10)
	raw := "\tfunc hello() {"

	pattern := regexp.MustCompile("(?i)hello")
	bgCode := "\033[43m"
	resetCode := "\033[0m"
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, bgCode, "", resetCode)

	if !strings.Contains(result, bgCode+"hello"+resetCode) {
		t.Errorf("expected yellow bg around 'hello' with tab expansion, got: %q", result)
	}

	if strings.Contains(result, bgCode+"func") {
		t.Errorf("func should not be highlighted, got: %q", result)
	}
}

func TestHighlightSearchSpans_WithANSI(t *testing.T) {
	gutter := "\033[48;2;30;50;30m\033[38;2;100;200;100m   1    2 \033[1m+\033[0m\033[48;2;30;50;30m"
	code := "\033[38;2;200;100;100mfunc\033[0m \033[38;2;200;200;200mhello\033[0m() {"
	inner := gutter + code
	raw := "func hello() {"

	pattern := regexp.MustCompile("(?i)hello")
	bgCode := "\033[43m"
	resetCode := "\033[0m"
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, bgCode, "", resetCode)

	if !strings.Contains(result, bgCode) {
		t.Errorf("expected yellow bg code in result, got: %q", result)
	}

	beforeMatch := result[:strings.Index(result, bgCode)]
	if strings.Contains(beforeMatch, "hello") {
		t.Errorf("hello should not appear before the bg code")
	}
}

func TestHighlightSearchSpans_MultipleMatches(t *testing.T) {
	gutter := "   1    2 +"
	code := "foo bar foo baz foo"
	inner := gutter + code + strings.Repeat(" ", 10)
	raw := "foo bar foo baz foo"

	pattern := regexp.MustCompile("foo")
	bgCode := "\033[43m"
	resetCode := "\033[0m"
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, bgCode, "", resetCode)

	count := strings.Count(result, bgCode+"foo"+resetCode)
	if count != 3 {
		t.Errorf("expected 3 highlighted 'foo' spans, got %d in: %q", count, result)
	}
}

func TestHighlightSearchSpans_NoMatch(t *testing.T) {
	inner := "   1    2 +func hello() {"
	raw := "func hello() {"

	pattern := regexp.MustCompile("zzzzz")
	bgCode := "\033[43m"
	resetCode := "\033[0m"
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, bgCode, "", resetCode)

	if result != inner {
		t.Errorf("expected unchanged inner when no match, got: %q", result)
	}
	_ = resetCode
}

func TestHighlightSearchSpans_ReplacesExistingBg(t *testing.T) {
	addBg := "\033[48;2;30;50;30m"
	yellowBg := "\033[48;2;215;153;33m"
	gutter := addBg + "\033[38;2;100;200;100m   1    2 \033[1m+\033[0m" + addBg
	code := "\033[38;2;200;100;100mfunc\033[0m" + addBg + " \033[38;2;200;200;200mhello\033[0m" + addBg + "() {"
	inner := gutter + code
	raw := "func hello() {"

	pattern := regexp.MustCompile("(?i)hello")
	gutterW := 11

	result := components.HighlightSearchSpans(inner, raw, pattern, gutterW, yellowBg, "", addBg)

	if !strings.Contains(result, yellowBg) {
		t.Errorf("expected yellow bg in result, got: %q", result)
	}

	hlStart := strings.Index(result, yellowBg)
	hlEnd := strings.Index(result[hlStart+len(yellowBg):], addBg)
	if hlEnd < 0 {
		t.Fatalf("could not find restore-bg after highlight in: %q", result)
	}
	hlRegion := result[hlStart : hlStart+len(yellowBg)+hlEnd]
	if strings.Contains(hlRegion[len(yellowBg):], addBg) {
		t.Errorf("add-bg should not appear inside highlighted match: %q", hlRegion)
	}
}
