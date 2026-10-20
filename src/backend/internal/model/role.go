package model

import "time"

type Role struct {
	RoleCode                string    `gorm:"column:role_code;primaryKey;size:32"`
	DisplayName             string    `gorm:"column:display_name;uniqueIndex;size:80"`
	Description             string    `gorm:"column:description;size:255"`
	AllowedOrganisationType string    `gorm:"column:allowed_organisation_type;size:40"`
	IsActive                bool      `gorm:"column:is_active"`
	CreatedAt               time.Time `gorm:"column:created_at"`
}

func (Role) TableName() string { return "roles" }
