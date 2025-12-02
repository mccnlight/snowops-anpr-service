package model

import (
	"github.com/google/uuid"
)

type UserRole string

const (
	UserRoleAkimatAdmin     UserRole = "AKIMAT_ADMIN"
	UserRoleAkimatUser      UserRole = "AKIMAT_USER"
	UserRoleKguZkhAdmin     UserRole = "KGU_ZKH_ADMIN"
	UserRoleKguZkhUser      UserRole = "KGU_ZKH_USER"
	UserRoleTooAdmin        UserRole = "TOO_ADMIN" // Deprecated: use LANDFILL_ADMIN
	UserRoleLandfillAdmin   UserRole = "LANDFILL_ADMIN"
	UserRoleLandfillUser    UserRole = "LANDFILL_USER"
	UserRoleContractorAdmin UserRole = "CONTRACTOR_ADMIN"
	UserRoleDriver          UserRole = "DRIVER"
)

type Principal struct {
	UserID   uuid.UUID
	OrgID    uuid.UUID
	Role     UserRole
	DriverID *uuid.UUID
}

func (p Principal) IsAkimat() bool {
	return p.Role == UserRoleAkimatAdmin || p.Role == UserRoleAkimatUser
}

func (p Principal) IsKgu() bool {
	return p.Role == UserRoleKguZkhAdmin || p.Role == UserRoleKguZkhUser
}

func (p Principal) IsToo() bool {
	return p.Role == UserRoleTooAdmin
}

// IsLandfill проверяет, является ли пользователь администратором или пользователем полигона
// Также поддерживает обратную совместимость с TOO_ADMIN
func (p Principal) IsLandfill() bool {
	return p.Role == UserRoleLandfillAdmin || p.Role == UserRoleLandfillUser || p.Role == UserRoleTooAdmin
}

// IsTechnicalOperator проверяет, является ли пользователь техническим оператором
// Поддерживает обратную совместимость с TOO_ADMIN и новые роли LANDFILL
func (p Principal) IsTechnicalOperator() bool {
	return p.Role == UserRoleTooAdmin || p.Role == UserRoleLandfillAdmin || p.Role == UserRoleLandfillUser
}

func (p Principal) IsContractor() bool {
	return p.Role == UserRoleContractorAdmin
}

func (p Principal) IsDriver() bool {
	return p.Role == UserRoleDriver
}

