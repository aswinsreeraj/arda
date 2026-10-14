package auth

import "fmt"

// Action represents an operation subject to role-based access control.
type Action string

const (
	ActionAgentCreate     Action = "agent:create"
	ActionAgentRead       Action = "agent:read"
	ActionAgentExecute    Action = "agent:execute"
	ActionVersionCreate   Action = "version:create"
	ActionVersionUpdate   Action = "version:update"
	ActionVersionPublish  Action = "version:publish"
	ActionVersionDelete   Action = "version:delete"
	ActionReliabilityGate Action = "reliability:gate"
)

// Policy defines the authorization policy matrix across all ARDA roles.
var Policy = map[Role]map[Action]bool{
	RoleSuperAdmin: {
		ActionAgentCreate:     true,
		ActionAgentRead:       true,
		ActionAgentExecute:    true,
		ActionVersionCreate:   true,
		ActionVersionUpdate:   true,
		ActionVersionPublish:  true,
		ActionVersionDelete:   true,
		ActionReliabilityGate: true,
	},
	RoleCompanyAdmin: {
		ActionAgentCreate:     true,
		ActionAgentRead:       true,
		ActionAgentExecute:    true,
		ActionVersionCreate:   true,
		ActionVersionUpdate:   true,
		ActionVersionPublish:  true,
		ActionVersionDelete:   true,
		ActionReliabilityGate: true,
	},
	RoleAgentDeveloper: {
		ActionAgentCreate:     true,
		ActionAgentRead:       true,
		ActionAgentExecute:    true,
		ActionVersionCreate:   true,
		ActionVersionUpdate:   true,
		ActionVersionPublish:  false, // Requires reviewer or admin approval
		ActionVersionDelete:   false,
		ActionReliabilityGate: true,
	},
	RoleReviewer: {
		ActionAgentCreate:     false,
		ActionAgentRead:       true,
		ActionAgentExecute:    false, // Reviewers evaluate and publish; they do not trigger operational executions
		ActionVersionCreate:   false,
		ActionVersionUpdate:   false,
		ActionVersionPublish:  true, // Reviewers evaluate and publish
		ActionVersionDelete:   false,
		ActionReliabilityGate: true,
	},
}

// Can checks whether the given role is authorized to perform the action.
func Can(role Role, action Action) bool {
	actions, ok := Policy[role]
	if !ok {
		return false
	}
	return actions[action]
}

// Authorize returns an error if the identity's role is not authorized for the action.
func Authorize(identity Identity, action Action) error {
	if !Can(identity.Role, action) {
		return fmt.Errorf("role %q is not authorized to perform action %q", identity.Role, action)
	}
	return nil
}
