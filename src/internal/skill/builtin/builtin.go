// Package builtin holds CodePilot built-in skill assets.
package builtin

import (
	"embed"
	"fmt"
	"path"
	"strings"
)

const DirName = "builtin"

const EmbeddedRoot = "embedded://internal/skill/builtin"

//go:embed */SKILL.md */reference/*.md
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

func IsEmbeddedPath(p string) bool {
	_, ok := EmbeddedRelativePath(p)
	return ok
}

func EmbeddedPath(skillName, rel string) string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	return EmbeddedRoot + "/" + strings.Trim(skillName, "/") + "/" + rel
}

func EmbeddedRelativePath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	prefix := EmbeddedRoot + "/"
	if !strings.HasPrefix(p, prefix) {
		return "", false
	}
	p = strings.TrimPrefix(p, prefix)
	if p == "" {
		return "", false
	}
	cleaned := path.Clean(p)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", false
	}
	return cleaned, true
}

func ReadEmbeddedFile(p string) ([]byte, error) {
	rel, ok := EmbeddedRelativePath(p)
	if !ok {
		return nil, fmt.Errorf("not a built-in embedded skill path: %s", p)
	}
	return embeddedFS.ReadFile(rel)
}
