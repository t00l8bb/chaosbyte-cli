package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileHappy(t *testing.T) {
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi there\n"), 0o644)
	tool := &readFileTool{ws: ws}
	got, err := tool.Run(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got.IsError {
		t.Errorf("unexpected error: %s", got.Content)
	}
	if got.Content != "hi there\n" {
		t.Errorf("content = %q", got.Content)
	}
}

func TestReadFileMissingArg(t *testing.T) {
	tool := &readFileTool{ws: t.TempDir()}
	got, _ := tool.Run(context.Background(), map[string]any{})
	if !got.IsError {
		t.Error("expected error for missing path arg")
	}
}

func TestReadFileEscapeRejected(t *testing.T) {
	tool := &readFileTool{ws: t.TempDir()}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd"} {
		got, _ := tool.Run(context.Background(), map[string]any{"path": bad})
		if !got.IsError {
			t.Errorf("expected escape rejection for %q", bad)
		}
	}
}

func TestReadFileTruncates(t *testing.T) {
	ws := t.TempDir()
	big := make([]byte, maxFileReadBytes+1024)
	for i := range big {
		big[i] = 'a'
	}
	_ = os.WriteFile(filepath.Join(ws, "big.txt"), big, 0o644)
	tool := &readFileTool{ws: ws}
	got, _ := tool.Run(context.Background(), map[string]any{"path": "big.txt"})
	if !strings.Contains(got.Content, "truncated") {
		t.Errorf("expected truncation note; got %d bytes back", len(got.Content))
	}
}

func TestWriteFileCreatesAndOverwrites(t *testing.T) {
	ws := t.TempDir()
	tool := &writeFileTool{ws: ws}
	got, _ := tool.Run(context.Background(), map[string]any{"path": "a/b/c.txt", "content": "first"})
	if got.IsError {
		t.Fatalf("write failed: %s", got.Content)
	}
	body, _ := os.ReadFile(filepath.Join(ws, "a/b/c.txt"))
	if string(body) != "first" {
		t.Errorf("first write content = %q", string(body))
	}
	got, _ = tool.Run(context.Background(), map[string]any{"path": "a/b/c.txt", "content": "second"})
	if got.IsError {
		t.Fatal(got.Content)
	}
	body, _ = os.ReadFile(filepath.Join(ws, "a/b/c.txt"))
	if string(body) != "second" {
		t.Errorf("overwrite content = %q", string(body))
	}
}

func TestListDirHappy(t *testing.T) {
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "alpha.txt"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(ws, "subdir"), 0o755)
	tool := &listDirTool{ws: ws}
	got, _ := tool.Run(context.Background(), map[string]any{"path": "."})
	if !strings.Contains(got.Content, "alpha.txt") {
		t.Errorf("missing alpha.txt in %q", got.Content)
	}
	if !strings.Contains(got.Content, "subdir/") {
		t.Errorf("missing subdir/ in %q", got.Content)
	}
}

func TestResolveInWorkspaceEscapes(t *testing.T) {
	ws := t.TempDir()
	cases := []string{"../etc/passwd", "/etc/passwd", "../"}
	for _, c := range cases {
		if _, err := resolveInWorkspace(ws, c); err == nil {
			t.Errorf("resolveInWorkspace(%q) should error", c)
		}
	}
}
