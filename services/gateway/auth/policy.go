package auth

import "fmt"

// Action represents an operation subject to role-based access control.
type Action string

const (
	ActionAgentCreate        Action = "agent:create"
	ActionAgentRead          Action = "agent:read"
	ActionAgentExecute       Action = "agent:execute"
	ActionVersionCreate      Action = "version:create"
	ActionVersionUpdate      Action = "version:update"
	ActionVersionPublish     Action = "version:publish"
	ActionVersionDelete      Action = "version:delete"
	ActionReliabilityGate    Action = "reliability:gate"
	ActionCredentialManage   Action = "credential:manage"
	ActionCredentialRead     Action = "credential:read"
	ActionTenantCreate       Action = "tenant:create"
	ActionTenantMemberManage Action = "tenant:member:manage"
	ActionTenantMemberRead   Action = "tenant:member:read"
	ActionToolCreate         Action = "tool:create"
	ActionToolRead           Action = "tool:read"
	ActionToolUpdate         Action = "tool:update"
	ActionToolDelete         Action = "tool:delete"
	ActionToolExecute        Action = "tool:execute"
	ActionAuditRead          Action = "audit:read"
)

// Policy defines the authorization policy matrix across all ARDA roles.
var Policy = map[Role]map[Action]bool{
	RoleSuperAdmin: {
		ActionAgentCreate:        false,
		ActionAgentRead:          false,
		ActionAgentExecute:       false,
		ActionVersionCreate:      false,
		ActionVersionUpdate:      false,
		ActionVersionPublish:     false,
		ActionVersionDelete:      false,
		ActionReliabilityGate:    false,
		ActionCredentialManage:   false,
		ActionCredentialRead:     false,
		ActionTenantCreate:       true,  // Full for /tenants platform management
		ActionTenantMemberManage: false, // Tenant scoped
		ActionTenantMemberRead:   false, // Tenant scoped
		ActionToolCreate:         false,
		ActionToolRead:           false,
		ActionToolUpdate:         false,
		ActionToolDelete:         false,
		ActionToolExecute:        false,
		ActionAuditRead:          false,
	},
	RoleCompanyAdmin: {
		ActionAgentCreate:        true,
		ActionAgentRead:          true,
		ActionAgentExecute:       true,
		ActionVersionCreate:      true,
		ActionVersionUpdate:      true,
		ActionVersionPublish:     true,
		ActionVersionDelete:      true,
		ActionReliabilityGate:    true,
		ActionCredentialManage:   true,
		ActionCredentialRead:     true,
		ActionTenantCreate:       false, // Platform tenants managed by super_admin
		ActionTenantMemberManage: true,  // Full for own tenant
		ActionTenantMemberRead:   true,
		ActionToolCreate:         true,
		ActionToolRead:           true,
		ActionToolUpdate:         true,
		ActionToolDelete:         true,
		ActionToolExecute:        true,
		ActionAuditRead:          true,
	},
	RoleAgentDeveloper: {
		ActionAgentCreate:        true,
		ActionAgentRead:          true,
		ActionAgentExecute:       true,
		ActionVersionCreate:      true,
		ActionVersionUpdate:      true,
		ActionVersionPublish:     false, // Requires reviewer or admin approval
		ActionVersionDelete:      false,
		ActionReliabilityGate:    true,
		ActionCredentialManage:   false, // Read-only per matrix
		ActionCredentialRead:     true,
		ActionTenantCreate:       false,
		ActionTenantMemberManage: false, // Self only via /auth/me
		ActionTenantMemberRead:   false,
		ActionToolCreate:         true,
		ActionToolRead:           true,
		ActionToolUpdate:         true,
		ActionToolDelete:         false, // Cannot delete tool definitions
		ActionToolExecute:        true,
		ActionAuditRead:          true,
	},
	RoleReviewer: {
		ActionAgentCreate:        false,
		ActionAgentRead:          true,
		ActionAgentExecute:       false,
		ActionVersionCreate:      false,
		ActionVersionUpdate:      false,
		ActionVersionPublish:     true, // Read + resolve
		ActionVersionDelete:      false,
		ActionReliabilityGate:    true,
		ActionCredentialManage:   false, // No access (-)
		ActionCredentialRead:     false,
		ActionTenantCreate:       false,
		ActionTenantMemberManage: false, // Self only via /auth/me
		ActionTenantMemberRead:   false,
		ActionToolCreate:         false,
		ActionToolRead:           true,
		ActionToolUpdate:         false,
		ActionToolDelete:         false,
		ActionToolExecute:        false,
		ActionAuditRead:          true,
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
