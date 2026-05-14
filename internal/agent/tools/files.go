package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bchayka/gitstatus/internal/agent"
)

// maxFileReadBytes caps a single read_file response so a runaway
// agent cannot blow up the conversation context.
const maxFileReadBytes = 32 * 1024

// readFileTool reads a file from the workspace and returns its
// contents (truncated to maxFileReadBytes with a note).
type readFileTool struct {
	ws string
}

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read a file from the workspace. Returns the file contents (truncated to 32 KB if larger)."
}
func (t *readFileTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"File path relative to the workspace root."}
  },
  "required":["path"]
}`)
}
func (t *readFileTool) Run(_ context.Context, args map[string]any) (agent.ToolResult, error) {
	rel, ok := args["path"].(string)
	if !ok || rel == "" {
		return agent.ToolResult{Content: "missing 'path' argument", IsError: true}, nil
	}
	full, err := resolveInWorkspace(t.ws, rel)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if len(body) > maxFileReadBytes {
		body = append(body[:maxFileReadBytes], []byte(fmt.Sprintf("\n... [truncated; full size %d bytes]", len(body)))...)
	}
	return agent.ToolResult{Content: string(body)}, nil
}

// writeFileTool writes (creates or overwrites) a file in the
// workspace.
type writeFileTool struct {
	ws string
}

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Description() string {
	return "Write (create or overwrite) a file in the workspace with the given content."
}
func (t *writeFileTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"File path relative to the workspace root."},
    "content":{"type":"string","description":"Full file contents to write."}
  },
  "required":["path","content"]
}`)
}
func (t *writeFileTool) Run(_ context.Context, args map[string]any) (agent.ToolResult, error) {
	rel, ok := args["path"].(string)
	if !ok || rel == "" {
		return agent.ToolResult{Content: "missing 'path' argument", IsError: true}, nil
	}
	content, ok := args["content"].(string)
	if !ok {
		return agent.ToolResult{Content: "missing 'content' argument", IsError: true}, nil
	}
	full, err := resolveInWorkspace(t.ws, rel)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("wrote %d bytes to %s", len(content), rel)}, nil
}

// listDirTool lists a directory inside the workspace.
type listDirTool struct {
	ws string
}

func (t *listDirTool) Name() string { return "list_dir" }
func (t *listDirTool) Description() string {
	return "List entries in a workspace directory. Returns names; '/' suffix marks directories."
}
func (t *listDirTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Directory path relative to the workspace root. Use '.' for the root."}
  },
  "required":["path"]
}`)
}
func (t *listDirTool) Run(_ context.Context, args map[string]any) (agent.ToolResult, error) {
	rel, _ := args["path"].(string)
	if rel == "" {
		rel = "."
	}
	full, err := resolveInWorkspace(t.ws, rel)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name())
		if e.IsDir() {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	return agent.ToolResult{Content: b.String()}, nil
}

// resolveInWorkspace turns a workspace-relative path into an absolute
// host-side filesystem path, refusing any path that escapes the
// workspace via "..", absolute references, or symlink traversal.
func resolveInWorkspace(workspace, rel string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("no workspace configured")
	}
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be relative to workspace; got %q", rel)
	}
	if strings.HasPrefix(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("path escapes workspace: %q", rel)
	}
	full := filepath.Join(workspace, cleaned)
	// Defense against symlink shenanigans: require the resolved path
	// to still live under workspace.
	wsAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(fullAbs, wsAbs) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return full, nil
}

