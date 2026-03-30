package main

import "strings"

type modelOption struct {
	ID      string
	Label   string
	Current bool
}

type modelPickerState struct {
	Open      bool
	Loading   bool
	Options   []modelOption
	Cursor    int
	LastError string
}

func parseModelList(content string) []modelOption {
	lines := strings.Split(content, "\n")
	options := make([]modelOption, 0)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}

		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if line == "" {
			continue
		}

		current := false
		if strings.HasSuffix(line, " (current)") {
			current = true
			line = strings.TrimSpace(strings.TrimSuffix(line, " (current)"))
		}

		id := line
		if idx := strings.Index(line, " ("); idx > 0 {
			id = line[:idx]
		}

		if id == "" {
			continue
		}

		options = append(options, modelOption{
			ID:      id,
			Label:   line,
			Current: current,
		})
	}

	if len(options) == 0 {
		return nil
	}

	return options
}
