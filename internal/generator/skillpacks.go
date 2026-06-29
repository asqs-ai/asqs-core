package generator

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed skillpacks/*.md
var skillPacksFS embed.FS

func loadSkillPack(name string) string {
	b, err := skillPacksFS.ReadFile("skillpacks/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func unitSkillPack() string { return loadSkillPack("unit-v1.md") }
func e2eSkillPack() string  { return loadSkillPack("e2e-v1.md") }
func docsSkillPack() string { return loadSkillPack("docs-v1.md") }

const skillPackFooter = "\n\nNOTE: If the user message contains repo-specific conventions under \"Repository documentation and agent skills\", those take precedence over the generic guidance above where they conflict."

func appendSkillPack(system, title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return system
	}
	if strings.TrimSpace(system) != "" {
		system += "\n\n"
	}
	return system + fmt.Sprintf("%s\n%s%s", title, body, skillPackFooter)
}
