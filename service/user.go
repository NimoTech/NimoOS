package service

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type UserService interface {
	GetUserRoleByID(id int) (string, error)
	// IsPathGranted reports whether the user has an explicit folder-level grant
	// that covers cleanPath (cleaned absolute path).
	IsPathGranted(userID int, cleanPath string) bool
}

type userService struct {
	db     *gorm.DB // nimoOS.db (main service DB)
	userDB *gorm.DB // user.db   (UserService DB — read-only for permission lookups)
}

// GetUserRoleByID queries the o_users table for the role of the given user ID.
// Returns "user" as default if the user has no role set.
func (u *userService) GetUserRoleByID(id int) (string, error) {
	var role string
	// Try user.db first (authoritative); fall back to nimoOS.db for compat.
	db := u.userDB
	if db == nil {
		db = u.db
	}
	result := db.Table("o_users").Select("role").Where("id = ?", id).Scan(&role)
	if result.Error != nil {
		return "", result.Error
	}
	if role == "" {
		role = "user"
	}
	return role, nil
}

// userFolderPermission mirrors the UserService model for read-only lookups.
type userFolderPermission struct {
	UserId int    `gorm:"column:user_id"`
	Path   string `gorm:"column:path"`
}

func (userFolderPermission) TableName() string { return "user_folder_permissions" }

// grantCache caches the set of granted paths per user to avoid hitting the DB
// on every single file API call. TTL = 30 s.
var grantCache struct {
	sync.RWMutex
	entries   map[int]grantCacheEntry
}

type grantCacheEntry struct {
	paths     []string
	expiresAt time.Time
}

// IsPathGranted returns true if userID has an explicit grant covering cleanPath.
func (u *userService) IsPathGranted(userID int, cleanPath string) bool {
	db := u.userDB
	if db == nil {
		return false
	}
	paths := u.grantedPaths(db, userID)
	for _, granted := range paths {
		g := filepath.Clean(granted)
		if cleanPath == g || strings.HasPrefix(cleanPath, g+"/") {
			return true
		}
	}
	return false
}

// grantedPaths returns the list of granted paths for userID, using a 30-second
// in-memory cache to avoid a DB round-trip on every file API call.
func (u *userService) grantedPaths(db *gorm.DB, userID int) []string {
	grantCache.RLock()
	if grantCache.entries != nil {
		if entry, ok := grantCache.entries[userID]; ok && time.Now().Before(entry.expiresAt) {
			grantCache.RUnlock()
			return entry.paths
		}
	}
	grantCache.RUnlock()

	// Refresh from DB.
	var perms []userFolderPermission
	db.Where("user_id = ?", userID).Find(&perms)
	paths := make([]string, 0, len(perms))
	for _, p := range perms {
		paths = append(paths, p.Path)
	}

	grantCache.Lock()
	if grantCache.entries == nil {
		grantCache.entries = make(map[int]grantCacheEntry)
	}
	grantCache.entries[userID] = grantCacheEntry{paths: paths, expiresAt: time.Now().Add(30 * time.Second)}
	grantCache.Unlock()

	return paths
}

// newUserService builds the UserService, optionally opening a secondary
// connection to user.db for folder-permission lookups.
func newUserService(mainDB *gorm.DB, userDBPath string) UserService {
	var userDB *gorm.DB
	if userDBPath != "" {
		db, err := gorm.Open(sqlite.Open(userDBPath+"?mode=ro"), &gorm.Config{})
		if err == nil {
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			userDB = db
		}
	}
	return &userService{db: mainDB, userDB: userDB}
}
