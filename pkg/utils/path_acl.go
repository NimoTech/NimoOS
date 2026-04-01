package utils

import (
	"path/filepath"
	"strings"
)

var userAllowedPrefixes = []string{"/DATA", "/mnt", "/media"}

// IsPathAllowed returns true if the given path is accessible for the given role.
// Admin users can access any non-empty path. Regular users are restricted to
// /DATA, /mnt, /media and their subdirectories.
func IsPathAllowed(path string, isAdmin bool) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	if isAdmin {
		return true
	}
	for _, prefix := range userAllowedPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}
