package aichat

import (
	"strings"
	"testing"
)

func TestEmphasizeMarksPlatformsAndCommands(t *testing.T) {
	got := emphasize("可以试试网易云音乐，或者用 /mode 切换。")
	if !strings.Contains(got, "<b>网易云音乐</b>") {
		t.Fatalf("platform not bolded: %q", got)
	}
	if !strings.Contains(got, "<code>/mode</code>") {
		t.Fatalf("command not chipped: %q", got)
	}
}

func TestEmphasizePrefersLongestToken(t *testing.T) {
	got := emphasize("YouTube Music 和 YouTube 都能搜")
	if !strings.Contains(got, "<b>YouTube Music</b>") {
		t.Fatalf("longest token should win: %q", got)
	}
	if strings.Contains(got, "<b><b>") {
		t.Fatalf("double bold detected: %q", got)
	}
}

func TestEmphasizeHandlesMarkdownBoldFromModel(t *testing.T) {
	got := emphasize("重点是 **不要编造歌名**，其余随意")
	if !strings.Contains(got, "<b>不要编造歌名</b>") {
		t.Fatalf("model bold not converted: %q", got)
	}
	if strings.Contains(got, "**") {
		t.Fatalf("raw markdown left behind: %q", got)
	}
}

func TestEmphasizeSkipsTokensInsideBoldRun(t *testing.T) {
	got := emphasize("**用 /mode 切换**")
	if strings.Count(got, "<b>") != 1 || strings.Count(got, "</b>") != 1 {
		t.Fatalf("unbalanced bold: %q", got)
	}
}

func TestEmphasizeLeavesPlainTextIntact(t *testing.T) {
	in := "今天下雨，适合听点慢的。"
	if got := emphasize(in); got != in {
		t.Fatalf("plain text changed: %q", got)
	}
}
