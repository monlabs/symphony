package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DynamicToolSpec describes a client-side tool exposed to Codex via thread/start.
type DynamicToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// LinearGraphQLToolName is the registered tool name.
const LinearGraphQLToolName = "linear_graphql"

// LinearGraphQLToolSpecs returns the dynamic tool specs to register with Codex.
func LinearGraphQLToolSpecs() []DynamicToolSpec {
	return []DynamicToolSpec{
		{
			Name:        LinearGraphQLToolName,
			Description: "Execute a raw GraphQL query or mutation against Linear using Symphony's configured auth.",
			InputSchema: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"query"},
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "GraphQL query or mutation document to execute against Linear.",
					},
					"variables": map[string]interface{}{
						"type":        []string{"object", "null"},
						"description": "Optional GraphQL variables object.",
						"additionalProperties": true,
					},
				},
			},
		},
	}
}

// ExecuteDynamicTool handles a tool call from Codex, dispatching to the right handler.
func ExecuteDynamicTool(toolName string, arguments interface{}, linearAPIKey string) map[string]interface{} {
	switch toolName {
	case LinearGraphQLToolName:
		return executeLinearGraphQL(arguments, linearAPIKey)
	default:
		return failureResponse(fmt.Sprintf("Unsupported dynamic tool: %s", toolName))
	}
}

func executeLinearGraphQL(arguments interface{}, apiKey string) map[string]interface{} {
	if apiKey == "" {
		return failureResponse("Symphony is missing Linear auth. Set linear.api_key in WORKFLOW.md or export LINEAR_API_KEY.")
	}

	query, variables, err := normalizeGraphQLArguments(arguments)
	if err != nil {
		return failureResponse(err.Error())
	}

	// Execute GraphQL request against Linear.
	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failureResponse("Failed to marshal GraphQL request: " + err.Error())
	}

	req, err := http.NewRequest("POST", "https://api.linear.app/graphql", strings.NewReader(string(body)))
	if err != nil {
		return failureResponse("Failed to create request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return failureResponse("Linear GraphQL request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return failureResponse("Failed to read Linear response: " + err.Error())
	}

	if resp.StatusCode != 200 {
		return failureResponse(fmt.Sprintf("Linear GraphQL request failed with HTTP %d: %s", resp.StatusCode, string(respBody)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return failureResponse("Failed to parse Linear response: " + err.Error())
	}

	// Check for GraphQL errors.
	success := true
	if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
		success = false
	}

	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	return map[string]interface{}{
		"success": success,
		"contentItems": []map[string]interface{}{
			{
				"type": "inputText",
				"text": string(resultJSON),
			},
		},
	}
}

func normalizeGraphQLArguments(arguments interface{}) (string, map[string]interface{}, error) {
	switch args := arguments.(type) {
	case string:
		trimmed := strings.TrimSpace(args)
		if trimmed == "" {
			return "", nil, fmt.Errorf("linear_graphql requires a non-empty query string")
		}
		return trimmed, map[string]interface{}{}, nil

	case map[string]interface{}:
		queryRaw, ok := args["query"]
		if !ok {
			return "", nil, fmt.Errorf("linear_graphql requires a 'query' field")
		}
		query, ok := queryRaw.(string)
		if !ok || strings.TrimSpace(query) == "" {
			return "", nil, fmt.Errorf("linear_graphql requires a non-empty query string")
		}

		variables := map[string]interface{}{}
		if vars, ok := args["variables"]; ok && vars != nil {
			if varsMap, ok := vars.(map[string]interface{}); ok {
				variables = varsMap
			}
		}

		return strings.TrimSpace(query), variables, nil

	default:
		return "", nil, fmt.Errorf("linear_graphql expects a query string or an object with query and optional variables")
	}
}

func failureResponse(message string) map[string]interface{} {
	errorPayload := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
		},
	}
	errorJSON, _ := json.MarshalIndent(errorPayload, "", "  ")
	return map[string]interface{}{
		"success": false,
		"contentItems": []map[string]interface{}{
			{
				"type": "inputText",
				"text": string(errorJSON),
			},
		},
	}
}
