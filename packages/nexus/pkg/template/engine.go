package template

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

var builtInTemplates = map[string]*Template{
	"node-postgres": {
		Name:        "node-postgres",
		Description: "React/Vue + Node API + PostgreSQL",
		Files: map[string]string{
			"docker-compose.yml": `version: '3.8'
services:
  web:
    image: node:18
    ports:
      - "3000:3000"
    volumes:
      - ".:/workspace"
    working_dir: /workspace/web
    command: npm run dev
    depends_on:
      - api
  api:
    image: node:18
    ports:
      - "5000:5000"
    volumes:
      - ".:/workspace"
    working_dir: /workspace/api
    command: npm run dev
    depends_on:
      - postgres
    environment:
      - DATABASE_URL=postgres://dev:dev@postgres:5432/dev
      - NODE_ENV=development
  postgres:
    image: postgres:15
    ports:
      - "5432:5432"
    environment:
      POSTGRES_DB: dev
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
`,
			".env.example": "# Database\nDATABASE_URL=postgres://dev:dev@localhost:5432/dev\n\n# API\nAPI_PORT=5000\nNODE_ENV=development\n\n# Web\nWEB_PORT=3000\n",
			"README.md":    "# Node.js + PostgreSQL Template\n\nA full-stack development environment with:\n- **web**: React/Vue frontend (port 3000)\n- **api**: Node.js API server (port 5000)\n- **postgres**: PostgreSQL database (port 5432)\n\n## Getting Started\n\n1. Copy `.env.example` to `.env` and configure\n2. Start services:\n   ```bash\n   docker-compose up -d\n   ```\n3. Set up the web app:\n   ```bash\n   cd web\n   npm install\n   npm run dev\n   ```\n4. Set up the API:\n   ```bash\n   cd api\n   npm install\n   npm run dev\n   ```\n\n## Services\n\n| Service | Port | Description |\n|---------|------|-------------|\n| web     | 3000 | Frontend dev server |\n| api     | 5000 | REST API server |\n| postgres| 5432 | PostgreSQL database |\n",
			"scripts/init.sh": `#!/bin/bash
set -e

echo "Initializing Node.js + PostgreSQL environment..."

# Wait for PostgreSQL to be ready
echo "Waiting for PostgreSQL..."
sleep 5

# Create database schema if needed
echo "Database initialized!"
`,
		},
	},
	"python-postgres": {
		Name:        "python-postgres",
		Description: "Flask/Django + PostgreSQL",
		Files: map[string]string{
			"docker-compose.yml": `version: '3.8'
services:
  web:
    image: python:3.11
    ports:
      - "8080:8080"
    volumes:
      - ".:/workspace"
    working_dir: /workspace
    command: python -m http.server 8080
  api:
    image: python:3.11
    ports:
      - "5000:5000"
    volumes:
      - ".:/workspace"
    working_dir: /workspace/api
    command: flask run --host=0.0.0.0 --port=5000
    depends_on:
      - postgres
    environment:
      - DATABASE_URL=postgresql://dev:dev@postgres:5432/dev
      - FLASK_APP=app.py
      - FLASK_ENV=development
  postgres:
    image: postgres:15
    ports:
      - "5432:5432"
    environment:
      POSTGRES_DB: dev
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
`,
			".env.example": "# Database\nDATABASE_URL=postgresql://dev:dev@localhost:5432/dev\n\n# Flask\nFLASK_APP=app.py\nFLASK_ENV=development\nFLASK_DEBUG=1\n\n# API\nAPI_PORT=5000\n\n# Web\nWEB_PORT=8080\n",
			"README.md":    "# Python + PostgreSQL Template\n\nA Python development environment with:\n- **web**: Static web server (port 8080)\n- **api**: Flask/Django API server (port 5000)\n- **postgres**: PostgreSQL database (port 5432)\n\n## Getting Started\n\n1. Copy `.env.example` to `.env` and configure\n2. Start services:\n   ```bash\n   docker-compose up -d\n   ```\n3. Set up the API:\n   ```bash\n   cd api\n   pip install -r requirements.txt\n   flask run\n   ```\n\n## Services\n\n| Service | Port | Description |\n|---------|------|-------------|\n| web     | 8080 | Static file server |\n| api     | 5000 | Flask API server |\n| postgres| 5432 | PostgreSQL database |\n",
			"scripts/init.sh": `#!/bin/bash
set -e

echo "Initializing Python + PostgreSQL environment..."

# Wait for PostgreSQL to be ready
echo "Waiting for PostgreSQL..."
sleep 5

echo "Database initialized!"
`,
		},
	},
	"go-postgres": {
		Name:        "go-postgres",
		Description: "Go API + PostgreSQL",
		Files: map[string]string{
			"docker-compose.yml": `version: '3.8'
services:
  api:
    image: golang:1.21
    ports:
      - "8080:8080"
    volumes:
      - ".:/workspace"
    working_dir: /workspace/api
    command: go run main.go
    depends_on:
      - postgres
    environment:
      - DATABASE_URL=postgres://dev:dev@postgres:5432/dev?sslmode=disable
  postgres:
    image: postgres:15
    ports:
      - "5432:5432"
    environment:
      POSTGRES_DB: dev
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  postgres_data:
`,
			".env.example": "# Database\nDATABASE_URL=postgres://dev:dev@localhost:5432/dev?sslmode=disable\n\n# API\nAPI_PORT=8080\n",
			"README.md":    "# Go + PostgreSQL Template\n\nA Go API development environment with:\n- **api**: Go REST API server (port 8080)\n- **postgres**: PostgreSQL database (port 5432)\n\n## Getting Started\n\n1. Copy `.env.example` to `.env` and configure\n2. Start services:\n   ```bash\n   docker-compose up -d\n   ```\n3. Set up the API:\n   ```bash\n   cd api\n   go mod download\n   go run main.go\n   ```\n\n## Services\n\n| Service | Port | Description |\n|---------|------|-------------|\n| api     | 8080 | Go REST API server |\n| postgres| 5432 | PostgreSQL database |\n",
			"scripts/init.sh": `#!/bin/bash
set -e

echo "Initializing Go + PostgreSQL environment..."

# Wait for PostgreSQL to be ready
echo "Waiting for PostgreSQL..."
sleep 5

echo "Database initialized!"
`,
		},
	},
}

type FileSystemEngine struct {
	templateDir string
}

func NewFileSystemEngine(templateDir string) *FileSystemEngine {
	return &FileSystemEngine{
		templateDir: templateDir,
	}
}

func NewEngine() *FileSystemEngine {
	return &FileSystemEngine{
		templateDir: "",
	}
}

func (e *FileSystemEngine) LoadTemplate(name string) (*Template, error) {
	if t, ok := builtInTemplates[name]; ok {
		return t, nil
	}

	if e.templateDir != "" {
		return e.loadFromDisk(name)
	}

	return nil, fmt.Errorf("template %q not found", name)
}

func (e *FileSystemEngine) loadFromDisk(name string) (*Template, error) {
	templatePath := filepath.Join(e.templateDir, name)

	info, err := os.Stat(templatePath)
	if err != nil {
		return nil, fmt.Errorf("template %q not found", name)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("template %q is not a directory", name)
	}

	files := make(map[string]string)

	err = filepath.Walk(templatePath, func(path string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(templatePath, path)
		if err != nil {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		files[relPath] = string(content)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to read template files: %w", err)
	}

	return &Template{
		Name:        name,
		Description: readDescription(files["README.md"]),
		Files:       files,
	}, nil
}

func readDescription(readme string) string {
	if readme == "" {
		return ""
	}
	lines := strings.Split(readme, "\n")
	if len(lines) > 1 {
		return strings.TrimPrefix(lines[1], "# ")
	}
	return ""
}

func (e *FileSystemEngine) ApplyTemplate(name, targetDir string, vars map[string]string) error {
	tmpl, err := e.LoadTemplate(name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	for filePath, content := range tmpl.Files {
		parsed, err := parseTemplateContent(content, vars)
		if err != nil {
			return fmt.Errorf("failed to parse template file %s: %w", filePath, err)
		}

		fullPath := filepath.Join(targetDir, filePath)
		dir := filepath.Dir(fullPath)

		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}

		if err := os.WriteFile(fullPath, []byte(parsed), 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", filePath, err)
		}
	}

	return nil
}

func parseTemplateContent(content string, vars map[string]string) (string, error) {
	if len(vars) == 0 {
		return content, nil
	}

	tmpl, err := template.New("").Parse(content)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (e *FileSystemEngine) ListTemplates() []TemplateInfo {
	infos := make([]TemplateInfo, 0, len(builtInTemplates))

	for name, tmpl := range builtInTemplates {
		files := make([]string, 0, len(tmpl.Files))
		for f := range tmpl.Files {
			files = append(files, f)
		}
		infos = append(infos, TemplateInfo{
			Name:        name,
			Description: tmpl.Description,
			Files:       files,
		})
	}

	if e.templateDir != "" {
		entries, err := os.ReadDir(e.templateDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					found := false
					for _, info := range infos {
						if info.Name == entry.Name() {
							found = true
							break
						}
					}
					if !found {
						infos = append(infos, TemplateInfo{
							Name:        entry.Name(),
							Description: "Custom template",
							Files:       []string{},
						})
					}
				}
			}
		}
	}

	return infos
}

func GetBuiltInTemplateNames() []string {
	names := make([]string, 0, len(builtInTemplates))
	for name := range builtInTemplates {
		names = append(names, name)
	}
	return names
}
