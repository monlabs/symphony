package agent

import (
	"fmt"
	"strings"

	"github.com/osteele/liquid"

	"github.com/monlabs/symphony/internal/domain"
)

// liquidEngine is a package-level Liquid engine instance (thread-safe).
var liquidEngine = liquid.NewEngine()

// DefaultPromptBuilder implements PromptBuilder using a Liquid-compatible template.
type DefaultPromptBuilder struct {
	template string
}

// NewDefaultPromptBuilder creates a DefaultPromptBuilder with the given template.
func NewDefaultPromptBuilder(template string) *DefaultPromptBuilder {
	return &DefaultPromptBuilder{template: template}
}

// UpdateTemplate updates the prompt template (for dynamic reload).
func (b *DefaultPromptBuilder) UpdateTemplate(template string) {
	b.template = template
}

// BuildPrompt implements PromptBuilder.
func (b *DefaultPromptBuilder) BuildPrompt(issue *domain.Issue, attempt *int) (string, error) {
	return BuildPrompt(b.template, issue, attempt)
}

// BuildPrompt renders a prompt template with the given issue and attempt.
// Uses Liquid-compatible semantics per Spec Section 5.4:
//   - Strict mode: unknown variables fail rendering.
//   - Unknown filters fail rendering.
//   - Supports {{ }}, {% for %}, {% if %}, filters, etc.
func BuildPrompt(template string, issue *domain.Issue, attempt *int) (string, error) {
	if strings.TrimSpace(template) == "" {
		return "You are working on an issue from Linear.", nil
	}

	vars := buildTemplateVars(issue, attempt)

	// Parse the template (catches syntax errors, unknown filters, etc.)
	tpl, err := liquidEngine.ParseTemplateAndCache([]byte(template), "", 0)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	// Render with strict variable checking via the Liquid engine.
	out, err := tpl.Render(vars)
	if err != nil {
		return "", fmt.Errorf("template render error: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

func buildTemplateVars(issue *domain.Issue, attempt *int) map[string]interface{} {
	issueMap := map[string]interface{}{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": nilStr(issue.Description),
		"priority":    nilInt(issue.Priority),
		"state":       issue.State,
		"branch_name": nilStr(issue.BranchName),
		"url":         nilStr(issue.URL),
		"labels":      issue.Labels,
	}

	blockers := make([]map[string]interface{}, len(issue.BlockedBy))
	for i, b := range issue.BlockedBy {
		blockers[i] = map[string]interface{}{
			"id":         nilStr(b.ID),
			"identifier": nilStr(b.Identifier),
			"state":      nilStr(b.State),
		}
	}
	issueMap["blocked_by"] = blockers

	if issue.CreatedAt != nil {
		issueMap["created_at"] = issue.CreatedAt.String()
	} else {
		issueMap["created_at"] = ""
	}
	if issue.UpdatedAt != nil {
		issueMap["updated_at"] = issue.UpdatedAt.String()
	} else {
		issueMap["updated_at"] = ""
	}

	vars := map[string]interface{}{
		"issue": issueMap,
	}
	if attempt != nil {
		vars["attempt"] = *attempt
	}

	return vars
}

func nilStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nilInt(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}
