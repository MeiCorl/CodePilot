// Package builtin holds CodePilot built-in skill assets.
package builtin

import "embed"

const DirName = "builtin"

//go:embed */SKILL.md
var embeddedFS embed.FS

type EmbeddedSkill struct {
	Name    string
	Path    string
	Content string
}

func Embedded() ([]EmbeddedSkill, error) {
	entries, err := embeddedFS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	out := make([]EmbeddedSkill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		relPath := name + "/SKILL.md"
		data, err := embeddedFS.ReadFile(relPath)
		if err != nil {
			return nil, err
		}
		out = append(out, EmbeddedSkill{Name: name, Path: relPath, Content: string(data)})
	}
	return out, nil
}
