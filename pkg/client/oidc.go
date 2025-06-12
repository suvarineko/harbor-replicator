package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// OIDC Group Operations

// ListOIDCGroups retrieves all OIDC groups
func (h *HarborClientWrapper) ListOIDCGroups(ctx context.Context) ([]OIDCGroup, error) {
	path := "/usergroups"
	
	resp, err := h.Get(ctx, path, WithQueryParams(map[string]string{
		"ldap_group_dn": "", // Filter for OIDC groups vs LDAP
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDC groups: %w", err)
	}
	defer resp.Body.Close()

	var groups []OIDCGroup
	if err := h.parseJSONResponse(resp, &groups); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC groups response: %w", err)
	}

	h.logger.Debug("Listed OIDC groups", "count", len(groups))
	return groups, nil
}

// GetOIDCGroup retrieves a specific OIDC group by ID
func (h *HarborClientWrapper) GetOIDCGroup(ctx context.Context, groupID int64) (*OIDCGroup, error) {
	path := fmt.Sprintf("/usergroups/%d", groupID)
	
	resp, err := h.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC group %d: %w", groupID, err)
	}
	defer resp.Body.Close()

	var group OIDCGroup
	if err := h.parseJSONResponse(resp, &group); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC group response: %w", err)
	}

	h.logger.Debug("Retrieved OIDC group", "group_id", groupID, "name", group.GroupName)
	return &group, nil
}

// CreateOIDCGroup creates a new OIDC group
func (h *HarborClientWrapper) CreateOIDCGroup(ctx context.Context, group *OIDCGroup) (*OIDCGroup, error) {
	if group == nil {
		return nil, fmt.Errorf("OIDC group cannot be nil")
	}

	// Validate group before creation
	if err := ValidateOIDCGroup(group); err != nil {
		return nil, fmt.Errorf("invalid OIDC group: %w", err)
	}

	path := "/usergroups"

	resp, err := h.Post(ctx, path, group)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC group: %w", err)
	}
	defer resp.Body.Close()

	var createdGroup OIDCGroup
	if err := h.parseJSONResponse(resp, &createdGroup); err != nil {
		return nil, fmt.Errorf("failed to parse created OIDC group response: %w", err)
	}

	h.logger.Info("Created OIDC group", "name", group.GroupName, "id", createdGroup.ID)
	return &createdGroup, nil
}

// UpdateOIDCGroup updates an existing OIDC group
func (h *HarborClientWrapper) UpdateOIDCGroup(ctx context.Context, groupID int64, group *OIDCGroup) (*OIDCGroup, error) {
	if group == nil {
		return nil, fmt.Errorf("OIDC group cannot be nil")
	}

	// Validate group before update
	if err := ValidateOIDCGroup(group); err != nil {
		return nil, fmt.Errorf("invalid OIDC group: %w", err)
	}

	path := fmt.Sprintf("/usergroups/%d", groupID)

	resp, err := h.Put(ctx, path, group)
	if err != nil {
		return nil, fmt.Errorf("failed to update OIDC group %d: %w", groupID, err)
	}
	defer resp.Body.Close()

	var updatedGroup OIDCGroup
	if err := h.parseJSONResponse(resp, &updatedGroup); err != nil {
		return nil, fmt.Errorf("failed to parse updated OIDC group response: %w", err)
	}

	h.logger.Info("Updated OIDC group", "group_id", groupID, "name", group.GroupName)
	return &updatedGroup, nil
}

// DeleteOIDCGroup deletes an OIDC group
func (h *HarborClientWrapper) DeleteOIDCGroup(ctx context.Context, groupID int64) error {
	path := fmt.Sprintf("/usergroups/%d", groupID)

	resp, err := h.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to delete OIDC group %d: %w", groupID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code when deleting OIDC group: %d", resp.StatusCode)
	}

	h.logger.Info("Deleted OIDC group", "group_id", groupID)
	return nil
}

// ListOIDCGroupsWithPagination retrieves OIDC groups with pagination support
func (h *HarborClientWrapper) ListOIDCGroupsWithPagination(ctx context.Context, options *OIDCGroupListOptions) (*OIDCGroupListResult, error) {
	if options == nil {
		options = &OIDCGroupListOptions{
			Page:     1,
			PageSize: 20,
		}
	}

	params := map[string]string{
		"page":      strconv.Itoa(options.Page),
		"page_size": strconv.Itoa(options.PageSize),
	}

	if options.GroupName != "" {
		params["group_name"] = options.GroupName
	}

	if options.GroupType != 0 {
		params["group_type"] = strconv.Itoa(options.GroupType)
	}

	if options.LdapGroupDN != "" {
		params["ldap_group_dn"] = options.LdapGroupDN
	}

	if options.Sort != "" {
		params["sort"] = options.Sort
	}

	resp, err := h.Get(ctx, "/usergroups", WithQueryParams(params))
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDC groups with pagination: %w", err)
	}
	defer resp.Body.Close()

	var groups []OIDCGroup
	if err := h.parseJSONResponse(resp, &groups); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC groups response: %w", err)
	}

	// Parse pagination headers
	result := &OIDCGroupListResult{
		Groups: groups,
		Page:   options.Page,
		Size:   options.PageSize,
		Total:  len(groups),
	}

	// Try to get total count from headers
	if totalHeader := resp.Header.Get("X-Total-Count"); totalHeader != "" {
		if total, err := strconv.Atoi(totalHeader); err == nil {
			result.Total = total
		}
	}

	h.logger.Debug("Listed OIDC groups with pagination", 
		"page", options.Page, 
		"size", options.PageSize, 
		"count", len(groups),
		"total", result.Total)

	return result, nil
}

// SearchOIDCGroups searches for OIDC groups by name
func (h *HarborClientWrapper) SearchOIDCGroups(ctx context.Context, query string) ([]OIDCGroup, error) {
	params := map[string]string{
		"group_name": query,
	}

	resp, err := h.Get(ctx, "/usergroups", WithQueryParams(params))
	if err != nil {
		return nil, fmt.Errorf("failed to search OIDC groups: %w", err)
	}
	defer resp.Body.Close()

	var groups []OIDCGroup
	if err := h.parseJSONResponse(resp, &groups); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC groups search response: %w", err)
	}

	h.logger.Debug("Searched OIDC groups", "query", query, "count", len(groups))
	return groups, nil
}

// Project Association Operations

// AddGroupToProject adds an OIDC group to a project with a specific role
func (h *HarborClientWrapper) AddGroupToProject(ctx context.Context, groupID, projectID int64, roleID int64) error {
	path := fmt.Sprintf("/projects/%d/members", projectID)

	member := map[string]interface{}{
		"member_group": map[string]interface{}{
			"id":         groupID,
			"group_type": 1, // OIDC group type
		},
		"role_id": roleID,
	}

	resp, err := h.Post(ctx, path, member)
	if err != nil {
		return fmt.Errorf("failed to add group %d to project %d: %w", groupID, projectID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code when adding group to project: %d", resp.StatusCode)
	}

	h.logger.Info("Added OIDC group to project", 
		"group_id", groupID, 
		"project_id", projectID, 
		"role_id", roleID)

	return nil
}

// RemoveGroupFromProject removes an OIDC group from a project
func (h *HarborClientWrapper) RemoveGroupFromProject(ctx context.Context, groupID, projectID int64) error {
	// First, get the member ID
	memberID, err := h.getProjectMemberID(ctx, projectID, groupID)
	if err != nil {
		return fmt.Errorf("failed to find group membership: %w", err)
	}

	path := fmt.Sprintf("/projects/%d/members/%d", projectID, memberID)

	resp, err := h.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to remove group %d from project %d: %w", groupID, projectID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code when removing group from project: %d", resp.StatusCode)
	}

	h.logger.Info("Removed OIDC group from project", 
		"group_id", groupID, 
		"project_id", projectID)

	return nil
}

// ListGroupProjectRoles lists all project roles for an OIDC group
func (h *HarborClientWrapper) ListGroupProjectRoles(ctx context.Context, groupID int64) ([]OIDCGroupProjectRole, error) {
	// This would typically require querying multiple projects or a dedicated endpoint
	// For now, we'll return the roles stored in the group structure
	group, err := h.GetOIDCGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("failed to get OIDC group for project roles: %w", err)
	}

	h.logger.Debug("Listed group project roles", "group_id", groupID, "roles_count", len(group.ProjectRoles))
	return group.ProjectRoles, nil
}

// UpdateGroupProjectRole updates a group's role in a specific project
func (h *HarborClientWrapper) UpdateGroupProjectRole(ctx context.Context, groupID, projectID, roleID int64) error {
	// Get member ID first
	memberID, err := h.getProjectMemberID(ctx, projectID, groupID)
	if err != nil {
		return fmt.Errorf("failed to find group membership: %w", err)
	}

	path := fmt.Sprintf("/projects/%d/members/%d", projectID, memberID)

	member := map[string]interface{}{
		"role_id": roleID,
	}

	resp, err := h.Put(ctx, path, member)
	if err != nil {
		return fmt.Errorf("failed to update group role in project: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code when updating group role: %d", resp.StatusCode)
	}

	h.logger.Info("Updated group project role", 
		"group_id", groupID, 
		"project_id", projectID, 
		"role_id", roleID)

	return nil
}

// Helper methods

// getProjectMemberID finds the member ID for a group in a project
func (h *HarborClientWrapper) getProjectMemberID(ctx context.Context, projectID, groupID int64) (int64, error) {
	path := fmt.Sprintf("/projects/%d/members", projectID)

	resp, err := h.Get(ctx, path, WithQueryParams(map[string]string{
		"entityname": strconv.FormatInt(groupID, 10),
	}))
	if err != nil {
		return 0, fmt.Errorf("failed to list project members: %w", err)
	}
	defer resp.Body.Close()

	var members []struct {
		ID         int64 `json:"id"`
		EntityID   int64 `json:"entity_id"`
		EntityType string `json:"entity_type"`
	}

	if err := h.parseJSONResponse(resp, &members); err != nil {
		return 0, fmt.Errorf("failed to parse project members response: %w", err)
	}

	for _, member := range members {
		if member.EntityID == groupID && member.EntityType == "g" {
			return member.ID, nil
		}
	}

	return 0, fmt.Errorf("group %d not found in project %d", groupID, projectID)
}

// Helper types for OIDC group operations

// OIDCGroupListOptions provides options for listing OIDC groups
type OIDCGroupListOptions struct {
	Page        int    `json:"page"`
	PageSize    int    `json:"page_size"`
	GroupName   string `json:"group_name,omitempty"`
	GroupType   int    `json:"group_type,omitempty"`
	LdapGroupDN string `json:"ldap_group_dn,omitempty"`
	Sort        string `json:"sort,omitempty"`
}

// OIDCGroupListResult contains paginated OIDC group results
type OIDCGroupListResult struct {
	Groups []OIDCGroup `json:"groups"`
	Page   int         `json:"page"`
	Size   int         `json:"size"`
	Total  int         `json:"total"`
}

// Validation helpers

// ValidateOIDCGroup validates an OIDC group before creation/update
func ValidateOIDCGroup(group *OIDCGroup) error {
	if group == nil {
		return fmt.Errorf("OIDC group cannot be nil")
	}

	if group.GroupName == "" {
		return fmt.Errorf("group name is required")
	}

	// Validate group name format (typically should not contain special characters)
	if strings.ContainsAny(group.GroupName, "!@#$%^&*()+={}[]|\\:;\"'<>?,./") {
		return fmt.Errorf("group name contains invalid characters")
	}

	// Group type validation (1 = OIDC, 2 = LDAP)
	if group.GroupType != 0 && group.GroupType != 1 && group.GroupType != 2 {
		return fmt.Errorf("invalid group type: must be 1 (OIDC) or 2 (LDAP)")
	}

	// Validate project roles if present
	for i, role := range group.ProjectRoles {
		if role.ProjectID <= 0 {
			return fmt.Errorf("project role %d: invalid project ID", i)
		}
		if role.RoleID <= 0 {
			return fmt.Errorf("project role %d: invalid role ID", i)
		}
	}

	return nil
}

// IsOIDCGroup checks if a group is an OIDC group (vs LDAP)
func IsOIDCGroup(group *OIDCGroup) bool {
	return group != nil && group.GroupType == 1
}

// IsLDAPGroup checks if a group is an LDAP group
func IsLDAPGroup(group *OIDCGroup) bool {
	return group != nil && group.GroupType == 2
}

// GetGroupProjectIDs returns all project IDs associated with the group
func GetGroupProjectIDs(group *OIDCGroup) []int64 {
	if group == nil {
		return nil
	}

	projectIDs := make([]int64, 0, len(group.ProjectRoles))
	for _, role := range group.ProjectRoles {
		projectIDs = append(projectIDs, role.ProjectID)
	}
	return projectIDs
}

// HasProjectAccess checks if the group has access to a specific project
func HasProjectAccess(group *OIDCGroup, projectID int64) bool {
	if group == nil {
		return false
	}

	for _, role := range group.ProjectRoles {
		if role.ProjectID == projectID {
			return true
		}
	}
	return false
}

// GetProjectRole returns the role ID for a specific project, or 0 if not found
func GetProjectRole(group *OIDCGroup, projectID int64) int64 {
	if group == nil {
		return 0
	}

	for _, role := range group.ProjectRoles {
		if role.ProjectID == projectID {
			return role.RoleID
		}
	}
	return 0
}

// BuildOIDCGroup creates a new OIDC group structure
func BuildOIDCGroup(groupName string) *OIDCGroup {
	return &OIDCGroup{
		GroupName:    groupName,
		GroupType:    1, // OIDC
		ProjectRoles: make([]OIDCGroupProjectRole, 0),
	}
}

// AddProjectRole adds a project role to an OIDC group
func (g *OIDCGroup) AddProjectRole(projectID, roleID int64, roleName string) {
	// Check if role already exists for this project
	for i, role := range g.ProjectRoles {
		if role.ProjectID == projectID {
			// Update existing role
			g.ProjectRoles[i].RoleID = roleID
			g.ProjectRoles[i].RoleName = roleName
			return
		}
	}

	// Add new role
	g.ProjectRoles = append(g.ProjectRoles, OIDCGroupProjectRole{
		ProjectID: projectID,
		RoleID:    roleID,
		RoleName:  roleName,
	})
}

// RemoveProjectRole removes a project role from an OIDC group
func (g *OIDCGroup) RemoveProjectRole(projectID int64) {
	for i, role := range g.ProjectRoles {
		if role.ProjectID == projectID {
			// Remove the role
			g.ProjectRoles = append(g.ProjectRoles[:i], g.ProjectRoles[i+1:]...)
			return
		}
	}
}

// Common role constants
const (
	RoleProjectAdmin     = 1
	RoleDeveloper        = 2
	RoleGuest            = 3
	RoleMaintainer       = 4
	RoleLimitedGuest     = 5
)

// GetRoleName returns the human-readable name for a role ID
func GetRoleName(roleID int64) string {
	switch roleID {
	case RoleProjectAdmin:
		return "projectAdmin"
	case RoleDeveloper:
		return "developer"
	case RoleGuest:
		return "guest"
	case RoleMaintainer:
		return "maintainer"
	case RoleLimitedGuest:
		return "limitedGuest"
	default:
		return "unknown"
	}
}