package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ErrMissingWorkflowFile indicates the WORKFLOW.md file was not found.
var ErrMissingWorkflowFile = errors.New("workflow file not found")

// ErrWorkflowParseError indicates a general failure reading or splitting the workflow file.
type ErrWorkflowParseError struct {
	Cause error
}

func (e *ErrWorkflowParseError) Error() string {
	return fmt.Sprintf("workflow parse error: %v", e.Cause)
}

func (e *ErrWorkflowParseError) Unwrap() error {
	return e.Cause
}

// ErrFrontMatterNotAMap indicates the YAML front matter did not decode to a map.
var ErrFrontMatterNotAMap = errors.New("YAML front matter must decode to a map")

// ErrTemplateParseError indicates the prompt template could not be parsed.
type ErrTemplateParseError struct {
	Cause error
}

func (e *ErrTemplateParseError) Error() string {
	return fmt.Sprintf("template parse error: %v", e.Cause)
}

func (e *ErrTemplateParseError) Unwrap() error {
	return e.Cause
}

// ErrTemplateRenderError indicates the prompt template could not be rendered.
type ErrTemplateRenderError struct {
	Cause error
}

func (e *ErrTemplateRenderError) Error() string {
	return fmt.Sprintf("template render error: %v", e.Cause)
}

func (e *ErrTemplateRenderError) Unwrap() error {
	return e.Cause
}

// ---------------------------------------------------------------------------
// WorkflowDefinition
// ---------------------------------------------------------------------------

// WorkflowDefinition represents the parsed contents of a WORKFLOW.md file.
type WorkflowDefinition struct {
	// Config is the raw YAML front matter parsed as a string-keyed map.
	Config map[string]interface{}
	// PromptTemplate is the Markdown body after the front matter, trimmed.
	PromptTemplate string
}

// ---------------------------------------------------------------------------
// LoadWorkflow
// ---------------------------------------------------------------------------

// LoadWorkflow reads a WORKFLOW.md file at the given path, splits the YAML
// front matter from the prompt body, and returns a WorkflowDefinition.
//
// Front matter rules:
//   - If the file starts with "---", everything until the next "---" line is
//     parsed as YAML. The remainder is the prompt template (trimmed).
//   - If no front matter delimiters are present, the entire file is the prompt
//     template and Config is an empty map.
//   - The YAML must decode to a map; a non-map value produces ErrFrontMatterNotAMap.
func LoadWorkflow(path string) (*WorkflowDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrMissingWorkflowFile
		}
		return nil, &ErrWorkflowParseError{Cause: err}
	}

	content := string(data)
	configMap, promptBody, err := splitFrontMatter(content)
	if err != nil {
		return nil, err
	}

	return &WorkflowDefinition{
		Config:         configMap,
		PromptTemplate: promptBody,
	}, nil
}

// splitFrontMatter separates YAML front matter from prompt body.
func splitFrontMatter(content string) (map[string]interface{}, string, error) {
	// Check if content starts with ---
	trimmed := strings.TrimLeft(content, " \t")
	if !strings.HasPrefix(trimmed, "---") {
		// No front matter: entire content is the prompt template.
		return map[string]interface{}{}, strings.TrimSpace(content), nil
	}

	// Find the opening delimiter line.
	// The opening --- may be followed by content on the same line (unlikely but handle it).
	// We split on lines to find the closing ---.
	lines := strings.Split(content, "\n")

	openIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			openIdx = i
			break
		}
	}
	if openIdx == -1 {
		// No clean --- line found despite prefix match; treat as no front matter.
		return map[string]interface{}{}, strings.TrimSpace(content), nil
	}

	// Find closing ---
	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}

	if closeIdx == -1 {
		// No closing delimiter; treat everything after the opener as YAML-only (edge case).
		// Per spec: parse until next ---; if none found, treat as parse error.
		return nil, "", &ErrWorkflowParseError{
			Cause: fmt.Errorf("opening --- found but no closing --- delimiter"),
		}
	}

	yamlContent := strings.Join(lines[openIdx+1:closeIdx], "\n")
	promptBody := strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))

	// Parse YAML
	configMap, err := parseYAMLFrontMatter(yamlContent)
	if err != nil {
		return nil, "", err
	}

	return configMap, promptBody, nil
}

// parseYAMLFrontMatter decodes YAML content into a map.
func parseYAMLFrontMatter(yamlContent string) (map[string]interface{}, error) {
	if strings.TrimSpace(yamlContent) == "" {
		return map[string]interface{}{}, nil
	}

	var raw interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		return nil, &ErrWorkflowParseError{Cause: fmt.Errorf("YAML decode: %w", err)}
	}

	if raw == nil {
		return map[string]interface{}{}, nil
	}

	result, ok := normalizeToStringMap(raw)
	if !ok {
		return nil, ErrFrontMatterNotAMap
	}

	return result, nil
}

// normalizeToStringMap recursively converts YAML-decoded values to map[string]interface{}.
func normalizeToStringMap(v interface{}) (map[string]interface{}, bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		// Recursively normalize nested maps.
		for k, val := range t {
			if sub, ok := val.(map[interface{}]interface{}); ok {
				t[k] = convertInterfaceMap(sub)
			}
		}
		return t, true
	case map[interface{}]interface{}:
		return convertInterfaceMap(t), true
	default:
		return nil, false
	}
}

// convertInterfaceMap converts map[interface{}]interface{} to map[string]interface{} recursively.
func convertInterfaceMap(m map[interface{}]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		key := fmt.Sprintf("%v", k)
		switch sub := v.(type) {
		case map[interface{}]interface{}:
			out[key] = convertInterfaceMap(sub)
		default:
			out[key] = sub
		}
	}
	return out
}
