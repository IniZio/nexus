package template

import "time"

type Template struct {
	Name        string
	Description string
	Files       map[string]string
	CreatedAt   time.Time
}

type TemplateInfo struct {
	Name        string
	Description string
	Files       []string
}

type TemplateEngine interface {
	LoadTemplate(name string) (*Template, error)
	ApplyTemplate(name, targetDir string, vars map[string]string) error
	ListTemplates() []TemplateInfo
}
