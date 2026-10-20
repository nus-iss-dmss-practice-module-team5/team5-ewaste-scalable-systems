package model

import "time"

type Organisation struct {
	OrganisationID   string    `gorm:"column:organisation_id;primaryKey;size:32"`
	OrganisationName string    `gorm:"column:organisation_name;uniqueIndex;size:160"`
	OrganisationType string    `gorm:"column:organisation_type;size:40"`
	Status           string    `gorm:"column:status;size:24"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (Organisation) TableName() string { return "organisations" }
