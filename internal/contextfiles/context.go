package contextfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContextFile defines a context file that should be loaded.
type ContextFile struct {
	Name        string // filename, e.g., "SOUL.md"
	Description string // what this file is for
}

// DefaultContextFiles is the priority-ordered list of context files to load.
// Files are loaded and concatenated in this order (first = highest priority).
var DefaultContextFiles = []ContextFile{
	{Name: "SOUL.md", Description: "Personality and identity definition"},
	{Name: "AGENTS.md", Description: "Operating instructions"},
	{Name: "CLAUDE.md", Description: "Claude-specific instructions"},
	{Name: ".hermes.md", Description: "Hermes-specific instructions"},
	{Name: "TOOLS.md", Description: "Tool usage conventions"},
	{Name: "README.md", Description: "General project context"},
}

// Load reads context files from the workspace directory and returns them
// concatenated with section headers. Returns empty string if no files found.
func Load(workspacePath string) (string, error) {
	if workspacePath == "" {
		return "", nil
	}

	var sections []string

	for _, cf := range DefaultContextFiles {
		path := filepath.Join(workspacePath, cf.Name)

		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return "", fmt.Errorf("read context file %s: %w", cf.Name, err)
		}

		// Skip empty files.
		if strings.TrimSpace(string(content)) == "" {
			continue
		}

		sections = append(sections, fmt.Sprintf("## %s\n\n%s", cf.Name, strings.TrimSpace(string(content))))
	}

	if len(sections) == 0 {
		return "", nil
	}

	return strings.Join(sections, "\n\n"), nil
}

// LoadCustom loads a specific context file by name.
func LoadCustom(workspacePath, filename string) (string, error) {
	if workspacePath == "" || filename == "" {
		return "", nil
	}

	path := filepath.Join(workspacePath, filename)

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}

		return "", fmt.Errorf("read custom context file %s: %w", filename, err)
	}

	return strings.TrimSpace(string(content)), nil
}

// Exists checks if any context files exist in the workspace.
func Exists(workspacePath string) bool {
	for _, cf := range DefaultContextFiles {
		path := filepath.Join(workspacePath, cf.Name)

		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	return false
}
