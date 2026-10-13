package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a procedural knowledge document.
type Skill struct {
	Name        string
	Description string
	Triggers    []string
	Content     string // full SKILL.md content
}

// LoadAll scans the skills directory and loads all SKILL.md files.
func LoadAll(workspacePath string) ([]Skill, error) {
	skillsDir := filepath.Join(workspacePath, "skills")

	_, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("stat skills dir: %w", err)
	}

	var skills []Skill

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")

		content, err := os.ReadFile(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return nil, fmt.Errorf("read skill %s: %w", entry.Name(), err)
		}

		skill, err := parseSkill(entry.Name(), string(content))
		if err != nil {
			return nil, fmt.Errorf("parse skill %s: %w", entry.Name(), err)
		}

		skills = append(skills, *skill)
	}

	return skills, nil
}

// Save writes a skill to the skills directory.
func Save(skill Skill, workspacePath string) error {
	skillDir := filepath.Join(workspacePath, "skills", skill.Name)

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")

	if err := os.WriteFile(skillPath, []byte(skill.Content), 0o644); err != nil {
		return fmt.Errorf("write skill file: %w", err)
	}

	return nil
}

// Delete removes a skill directory.
func Delete(name, workspacePath string) error {
	skillDir := filepath.Join(workspacePath, "skills", name)

	if err := os.RemoveAll(skillDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove skill dir: %w", err)
	}

	return nil
}

// FormatPromptSummary returns a compact summary of all skills for the system prompt.
func FormatPromptSummary(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Skills\n\n")
	sb.WriteString("You have the following skills. Use them when relevant to the user's request.\n\n")

	for _, skill := range skills {
		fmt.Fprintf(&sb, "- **%s**: %s", skill.Name, skill.Description)

		if len(skill.Triggers) > 0 {
			fmt.Fprintf(&sb, " (triggers: %s)", strings.Join(skill.Triggers, ", "))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}

// MatchTriggers returns skills whose triggers match the query.
func MatchTriggers(skills []Skill, query string) []Skill {
	queryLower := strings.ToLower(query)

	var matches []Skill

	for _, skill := range skills {
		for _, trigger := range skill.Triggers {
			if strings.Contains(queryLower, strings.ToLower(trigger)) {
				matches = append(matches, skill)

				break
			}
		}
	}

	return matches
}

// parseSkill extracts frontmatter and content from a SKILL.md file.
func parseSkill(name, content string) (*Skill, error) {
	skill := &Skill{
		Name:    name,
		Content: content,
	}

	// Parse YAML frontmatter if present.
	if !strings.HasPrefix(content, "---") {
		return skill, nil
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return skill, nil
	}

	frontmatter := parts[1]

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "description:") {
			skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "triggers:") {
			triggersStr := strings.TrimSpace(strings.TrimPrefix(line, "triggers:"))

			triggersStr = strings.Trim(triggersStr, "[]")
			for _, t := range strings.Split(triggersStr, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					skill.Triggers = append(skill.Triggers, t)
				}
			}
		}
	}

	return skill, nil
}
