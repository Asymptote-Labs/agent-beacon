package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowInput struct {
	Description string  `yaml:"description"`
	Required    bool    `yaml:"required"`
	Default     *string `yaml:"default"`
	Type        string  `yaml:"type"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type workflowFile struct {
	Name    string `yaml:"name"`
	RunName string `yaml:"run-name"`
	On      struct {
		WorkflowDispatch struct {
			Inputs map[string]workflowInput `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Env   map[string]string `yaml:"env"`
		Steps []workflowStep    `yaml:"steps"`
	} `yaml:"jobs"`
}

// loadWorkflow parses the workflow this package dispatches, from this checkout.
func loadWorkflow(t *testing.T) workflowFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", Workflow))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("parse %s: %v", Workflow, err)
	}
	return wf
}

// The dispatch only works if the workflow declares the correlation input and renders it into the
// run's title in the form pickOwnRun matches. Both sides live in different files, so this pins
// them together: change one without the other and this fails here rather than as a dispatch that
// never finds its run.
func TestWorkflowStampsTheCorrelationIDIntoTheRunName(t *testing.T) {
	wf := loadWorkflow(t)

	in, ok := wf.On.WorkflowDispatch.Inputs[correlationInput]
	if !ok {
		t.Fatalf("%s must declare the %s workflow_dispatch input", Workflow, correlationInput)
	}
	// Optional with an empty default, so a manual dispatch from the Actions tab still works and
	// keeps the plain title.
	if in.Required {
		t.Errorf("%s must be optional so manual dispatches keep working", correlationInput)
	}
	if in.Type != "string" {
		t.Errorf("%s type = %q, want string", correlationInput, in.Type)
	}
	if in.Default == nil || *in.Default != "" {
		t.Errorf("%s must default to empty", correlationInput)
	}

	// The run-name's formatted branch, rendered for an id, must produce exactly the marker the
	// selection looks for; its fallback must be the workflow name, which is what a run without a
	// run-name is titled and what a manual dispatch therefore still shows.
	m := regexp.MustCompile(`^\$\{\{\s*inputs\.` + correlationInput +
		`\s*&&\s*format\('([^']*)',\s*inputs\.` + correlationInput + `\)\s*\|\|\s*'([^']*)'\s*\}\}$`).
		FindStringSubmatch(strings.TrimSpace(wf.RunName))
	if m == nil {
		t.Fatalf("run-name must render inputs.%s with format(...) and fall back to a plain "+
			"title; got %q", correlationInput, wf.RunName)
	}
	const id = "0123456789abcdef01234567"
	rendered := strings.ReplaceAll(m[1], "{0}", id)
	if got, err := pickOwnRun([]ghRun{{DatabaseID: 2, DisplayTitle: rendered}}, 1, id); err != nil || got != 2 {
		t.Errorf("a run titled %q must be selected for id %s (got %d, %v)", rendered, id, got, err)
	}
	if want := renderRunName(map[string]bool{correlationInput: true},
		map[string]string{correlationInput: id}, false); rendered != want {
		t.Errorf("the fake GitHub titles runs %q but the workflow renders %q; keep them in step",
			want, rendered)
	}
	if m[2] != wf.Name {
		t.Errorf("run-name fallback = %q, want the workflow name %q", m[2], wf.Name)
	}
}

// The id is only ever rendered into the title. Keeping it out of every script, env block and
// action input means an arbitrary string typed into the dispatch form cannot reach a shell, so the
// input needs none of the validation scenario and claude_version get.
func TestWorkflowNeverPassesTheCorrelationIDToAStep(t *testing.T) {
	wf := loadWorkflow(t)
	if len(wf.Jobs) == 0 {
		t.Fatal("parsed no jobs; the workflow shape changed and this test is no longer checking anything")
	}
	for name, job := range wf.Jobs {
		for k, v := range job.Env {
			if strings.Contains(v, correlationInput) {
				t.Errorf("job %s env %s references %s", name, k, correlationInput)
			}
		}
		for _, step := range job.Steps {
			fields := []string{step.Run}
			for _, v := range step.Env {
				fields = append(fields, v)
			}
			for _, v := range step.With {
				fields = append(fields, v)
			}
			for _, f := range fields {
				if strings.Contains(f, correlationInput) {
					t.Errorf("job %s step %q references %s", name, step.Name, correlationInput)
				}
			}
		}
	}
}
