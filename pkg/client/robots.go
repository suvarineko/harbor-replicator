package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Robot Account Operations

// ListSystemRobotAccounts retrieves all system-level robot accounts
func (h *HarborClientWrapper) ListSystemRobotAccounts(ctx context.Context) ([]RobotAccount, error) {
	path := "/robots"
	
	resp, err := h.Get(ctx, path, WithQueryParams(map[string]string{
		"level": "system",
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to list system robot accounts: %w", err)
	}
	defer resp.Body.Close()

	var robots []RobotAccount
	if err := h.parseJSONResponse(resp, &robots); err != nil {
		return nil, fmt.Errorf("failed to parse robot accounts response: %w", err)
	}

	h.logger.Debug("Listed system robot accounts", "count", len(robots))
	return robots, nil
}

// ListProjectRobotAccounts retrieves robot accounts for a specific project
func (h *HarborClientWrapper) ListProjectRobotAccounts(ctx context.Context, projectID int64) ([]RobotAccount, error) {
	path := fmt.Sprintf("/projects/%d/robots", projectID)
	
	resp, err := h.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to list project robot accounts for project %d: %w", projectID, err)
	}
	defer resp.Body.Close()

	var robots []RobotAccount
	if err := h.parseJSONResponse(resp, &robots); err != nil {
		return nil, fmt.Errorf("failed to parse robot accounts response: %w", err)
	}

	h.logger.Debug("Listed project robot accounts", "project_id", projectID, "count", len(robots))
	return robots, nil
}

// GetRobotAccount retrieves a specific robot account by ID
func (h *HarborClientWrapper) GetRobotAccount(ctx context.Context, robotID int64) (*RobotAccount, error) {
	path := fmt.Sprintf("/robots/%d", robotID)
	
	resp, err := h.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get robot account %d: %w", robotID, err)
	}
	defer resp.Body.Close()

	var robot RobotAccount
	if err := h.parseJSONResponse(resp, &robot); err != nil {
		return nil, fmt.Errorf("failed to parse robot account response: %w", err)
	}

	h.logger.Debug("Retrieved robot account", "robot_id", robotID, "name", robot.Name)
	return &robot, nil
}

// CreateRobotAccount creates a new robot account
func (h *HarborClientWrapper) CreateRobotAccount(ctx context.Context, robot *RobotAccount) (*RobotAccount, error) {
	if robot == nil {
		return nil, fmt.Errorf("robot account cannot be nil")
	}

	var path string
	if robot.Level == "system" {
		path = "/robots"
	} else {
		// Extract project ID from permissions if project-level
		projectID := h.extractProjectIDFromPermissions(robot.Permissions)
		if projectID == 0 {
			return nil, fmt.Errorf("project ID is required for project-level robot accounts")
		}
		path = fmt.Sprintf("/projects/%d/robots", projectID)
	}

	resp, err := h.Post(ctx, path, robot)
	if err != nil {
		return nil, fmt.Errorf("failed to create robot account: %w", err)
	}
	defer resp.Body.Close()

	var createdRobot RobotAccount
	if err := h.parseJSONResponse(resp, &createdRobot); err != nil {
		return nil, fmt.Errorf("failed to parse created robot account response: %w", err)
	}

	h.logger.Info("Created robot account", "name", robot.Name, "level", robot.Level, "id", createdRobot.ID)
	return &createdRobot, nil
}

// UpdateRobotAccount updates an existing robot account
func (h *HarborClientWrapper) UpdateRobotAccount(ctx context.Context, robotID int64, robot *RobotAccount) (*RobotAccount, error) {
	if robot == nil {
		return nil, fmt.Errorf("robot account cannot be nil")
	}

	path := fmt.Sprintf("/robots/%d", robotID)

	resp, err := h.Put(ctx, path, robot)
	if err != nil {
		return nil, fmt.Errorf("failed to update robot account %d: %w", robotID, err)
	}
	defer resp.Body.Close()

	var updatedRobot RobotAccount
	if err := h.parseJSONResponse(resp, &updatedRobot); err != nil {
		return nil, fmt.Errorf("failed to parse updated robot account response: %w", err)
	}

	h.logger.Info("Updated robot account", "robot_id", robotID, "name", robot.Name)
	return &updatedRobot, nil
}

// DeleteRobotAccount deletes a robot account
func (h *HarborClientWrapper) DeleteRobotAccount(ctx context.Context, robotID int64) error {
	path := fmt.Sprintf("/robots/%d", robotID)

	resp, err := h.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to delete robot account %d: %w", robotID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code when deleting robot account: %d", resp.StatusCode)
	}

	h.logger.Info("Deleted robot account", "robot_id", robotID)
	return nil
}

// RegenerateRobotSecret regenerates the secret for a robot account
func (h *HarborClientWrapper) RegenerateRobotSecret(ctx context.Context, robotID int64) (*RobotAccount, error) {
	path := fmt.Sprintf("/robots/%d", robotID)

	// Send PATCH request to regenerate secret
	req := map[string]interface{}{
		"secret": "",
	}

	resp, err := h.makeRequest(ctx, "PATCH", path, req)
	if err != nil {
		return nil, fmt.Errorf("failed to regenerate robot secret for %d: %w", robotID, err)
	}
	defer resp.Body.Close()

	var robot RobotAccount
	if err := h.parseJSONResponse(resp, &robot); err != nil {
		return nil, fmt.Errorf("failed to parse regenerated robot account response: %w", err)
	}

	h.logger.Info("Regenerated robot account secret", "robot_id", robotID)
	return &robot, nil
}

// ListRobotAccountsWithPagination retrieves robot accounts with pagination support
func (h *HarborClientWrapper) ListRobotAccountsWithPagination(ctx context.Context, options *RobotListOptions) (*RobotListResult, error) {
	if options == nil {
		options = &RobotListOptions{
			Page:     1,
			PageSize: 20,
		}
	}

	params := map[string]string{
		"page":      strconv.Itoa(options.Page),
		"page_size": strconv.Itoa(options.PageSize),
	}

	if options.Level != "" {
		params["level"] = options.Level
	}

	if options.Query != "" {
		params["q"] = options.Query
	}

	if options.Sort != "" {
		params["sort"] = options.Sort
	}

	var path string
	if options.ProjectID > 0 {
		path = fmt.Sprintf("/projects/%d/robots", options.ProjectID)
	} else {
		path = "/robots"
	}

	resp, err := h.Get(ctx, path, WithQueryParams(params))
	if err != nil {
		return nil, fmt.Errorf("failed to list robot accounts with pagination: %w", err)
	}
	defer resp.Body.Close()

	var robots []RobotAccount
	if err := h.parseJSONResponse(resp, &robots); err != nil {
		return nil, fmt.Errorf("failed to parse robot accounts response: %w", err)
	}

	// Parse pagination headers
	result := &RobotListResult{
		Robots: robots,
		Page:   options.Page,
		Size:   options.PageSize,
		Total:  len(robots), // This would normally come from a header like X-Total-Count
	}

	// Try to get total count from headers
	if totalHeader := resp.Header.Get("X-Total-Count"); totalHeader != "" {
		if total, err := strconv.Atoi(totalHeader); err == nil {
			result.Total = total
		}
	}

	h.logger.Debug("Listed robot accounts with pagination", 
		"page", options.Page, 
		"size", options.PageSize, 
		"count", len(robots),
		"total", result.Total)

	return result, nil
}

// SearchRobotAccounts searches for robot accounts by name or description
func (h *HarborClientWrapper) SearchRobotAccounts(ctx context.Context, query string, level string) ([]RobotAccount, error) {
	params := map[string]string{
		"q": query,
	}

	if level != "" {
		params["level"] = level
	}

	resp, err := h.Get(ctx, "/robots", WithQueryParams(params))
	if err != nil {
		return nil, fmt.Errorf("failed to search robot accounts: %w", err)
	}
	defer resp.Body.Close()

	var robots []RobotAccount
	if err := h.parseJSONResponse(resp, &robots); err != nil {
		return nil, fmt.Errorf("failed to parse robot accounts search response: %w", err)
	}

	h.logger.Debug("Searched robot accounts", "query", query, "level", level, "count", len(robots))
	return robots, nil
}

// Helper types for robot account operations

// RobotListOptions provides options for listing robot accounts
type RobotListOptions struct {
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	Level     string `json:"level,omitempty"`     // "system" or "project"
	Query     string `json:"query,omitempty"`     // Search query
	Sort      string `json:"sort,omitempty"`      // Sort field
	ProjectID int64  `json:"project_id,omitempty"` // For project-level robots
}

// RobotListResult contains paginated robot account results
type RobotListResult struct {
	Robots []RobotAccount `json:"robots"`
	Page   int            `json:"page"`
	Size   int            `json:"size"`
	Total  int            `json:"total"`
}

// Helper methods

// extractProjectIDFromPermissions extracts project ID from robot permissions
func (h *HarborClientWrapper) extractProjectIDFromPermissions(permissions []RobotPermission) int64 {
	for _, perm := range permissions {
		if strings.HasPrefix(perm.Namespace, "/project/") {
			projectIDStr := strings.TrimPrefix(perm.Namespace, "/project/")
			if projectID, err := strconv.ParseInt(projectIDStr, 10, 64); err == nil {
				return projectID
			}
		}
	}
	return 0
}

// parseJSONResponse parses a JSON response into the target structure
func (h *HarborClientWrapper) parseJSONResponse(resp *http.Response, target interface{}) error {
	if resp == nil {
		return fmt.Errorf("response is nil")
	}

	if resp.StatusCode >= 400 {
		// Try to read error response
		bodyBytes, _ := io.ReadAll(resp.Body)
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("API error: %s", string(bodyBytes)),
			Retryable:  resp.StatusCode >= 500,
		}
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if len(bodyBytes) == 0 {
		return fmt.Errorf("empty response body")
	}

	if err := json.Unmarshal(bodyBytes, target); err != nil {
		return fmt.Errorf("failed to unmarshal JSON response: %w", err)
	}

	return nil
}

// Validation helpers

// ValidateRobotAccount validates a robot account before creation/update
func ValidateRobotAccount(robot *RobotAccount) error {
	if robot == nil {
		return fmt.Errorf("robot account cannot be nil")
	}

	if robot.Name == "" {
		return fmt.Errorf("robot name is required")
	}

	if robot.Level != "system" && robot.Level != "project" {
		return fmt.Errorf("robot level must be 'system' or 'project'")
	}

	if len(robot.Permissions) == 0 {
		return fmt.Errorf("robot must have at least one permission")
	}

	// Validate permissions
	for i, perm := range robot.Permissions {
		if perm.Kind == "" {
			return fmt.Errorf("permission %d: kind is required", i)
		}

		if perm.Namespace == "" {
			return fmt.Errorf("permission %d: namespace is required", i)
		}

		if len(perm.Access) == 0 {
			return fmt.Errorf("permission %d: at least one access right is required", i)
		}

		for j, access := range perm.Access {
			if access.Resource == "" {
				return fmt.Errorf("permission %d, access %d: resource is required", i, j)
			}
			if access.Action == "" {
				return fmt.Errorf("permission %d, access %d: action is required", i, j)
			}
		}
	}

	return nil
}

// IsSystemRobot checks if a robot account is system-level
func IsSystemRobot(robot *RobotAccount) bool {
	return robot != nil && robot.Level == "system"
}

// IsProjectRobot checks if a robot account is project-level
func IsProjectRobot(robot *RobotAccount) bool {
	return robot != nil && robot.Level == "project"
}

// GetRobotProjectID extracts project ID from a project-level robot account
func GetRobotProjectID(robot *RobotAccount) int64 {
	if !IsProjectRobot(robot) {
		return 0
	}

	for _, perm := range robot.Permissions {
		if strings.HasPrefix(perm.Namespace, "/project/") {
			projectIDStr := strings.TrimPrefix(perm.Namespace, "/project/")
			if projectID, err := strconv.ParseInt(projectIDStr, 10, 64); err == nil {
				return projectID
			}
		}
	}
	return 0
}

// BuildRobotPermission builds a robot permission structure
func BuildRobotPermission(kind, namespace string, accesses []RobotAccess) RobotPermission {
	return RobotPermission{
		Kind:      kind,
		Namespace: namespace,
		Access:    accesses,
	}
}

// BuildRobotAccess builds a robot access structure
func BuildRobotAccess(resource, action string) RobotAccess {
	return RobotAccess{
		Resource: resource,
		Action:   action,
	}
}

// Common permission builders

// BuildSystemRobotPermissions builds common system-level robot permissions
func BuildSystemRobotPermissions() []RobotPermission {
	return []RobotPermission{
		BuildRobotPermission("system", "/", []RobotAccess{
			BuildRobotAccess("repository", "pull"),
			BuildRobotAccess("repository", "push"),
		}),
	}
}

// BuildProjectRobotPermissions builds common project-level robot permissions
func BuildProjectRobotPermissions(projectID int64) []RobotPermission {
	namespace := fmt.Sprintf("/project/%d", projectID)
	return []RobotPermission{
		BuildRobotPermission("project", namespace, []RobotAccess{
			BuildRobotAccess("repository", "pull"),
			BuildRobotAccess("repository", "push"),
			BuildRobotAccess("artifact", "read"),
			BuildRobotAccess("artifact", "create"),
		}),
	}
}