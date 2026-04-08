package service

import (
	"gorm.io/gorm"
)

type UserService interface {
	GetUserRoleByID(id int) (string, error)
}

type userService struct {
	db *gorm.DB
}

// GetUserRoleByID queries the o_users table for the role of the given user ID.
// Returns "user" as default if the user has no role set.
func (u *userService) GetUserRoleByID(id int) (string, error) {
	var role string
	result := u.db.Table("o_users").Select("role").Where("id = ?", id).Scan(&role)
	if result.Error != nil {
		return "", result.Error
	}
	if role == "" {
		role = "user"
	}
	return role, nil
}
