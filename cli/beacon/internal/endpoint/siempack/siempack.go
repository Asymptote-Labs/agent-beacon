package siempack

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const logPathToken = "{{LOG_PATH}}"

type File struct {
	Name            string
	Content         string
	TemplateLogPath bool
	JSONEscape      bool
}

func ReadFile(fsys fs.FS, path string) (string, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func RenderLogPath(content, logPath string) string {
	return strings.ReplaceAll(content, logPathToken, logPath)
}

// JSONEscapeForString escapes s for safe embedding inside a JSON string literal
// (the surrounding quotes are not included).
func JSONEscapeForString(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func Install(outputDir string, files []File, logPath string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	for _, file := range files {
		content := file.Content
		if file.TemplateLogPath {
			lp := logPath
			if file.JSONEscape {
				lp = JSONEscapeForString(lp)
			}
			content = RenderLogPath(content, lp)
		}
		if err := os.WriteFile(filepath.Join(outputDir, file.Name), []byte(content), Mode(file.Name)); err != nil {
			return err
		}
	}
	return nil
}

func Mode(name string) os.FileMode {
	if strings.HasSuffix(name, ".sh") {
		return 0755
	}
	return 0644
}
