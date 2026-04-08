package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPathAllowed_Admin(t *testing.T) {
	assert.True(t, IsPathAllowed("/", true))
	assert.True(t, IsPathAllowed("/etc/passwd", true))
	assert.True(t, IsPathAllowed("/root", true))
}

func TestIsPathAllowed_UserAllowedPaths(t *testing.T) {
	assert.True(t, IsPathAllowed("/DATA", false))
	assert.True(t, IsPathAllowed("/DATA/Documents", false))
	assert.True(t, IsPathAllowed("/DATA/Documents/file.txt", false))
	assert.True(t, IsPathAllowed("/mnt", false))
	assert.True(t, IsPathAllowed("/mnt/disk1", false))
	assert.True(t, IsPathAllowed("/media", false))
	assert.True(t, IsPathAllowed("/media/usb0/photo.jpg", false))
}

func TestIsPathAllowed_UserDeniedPaths(t *testing.T) {
	assert.False(t, IsPathAllowed("/", false))
	assert.False(t, IsPathAllowed("/etc", false))
	assert.False(t, IsPathAllowed("/etc/passwd", false))
	assert.False(t, IsPathAllowed("/root", false))
	assert.False(t, IsPathAllowed("/home", false))
	assert.False(t, IsPathAllowed("/var/log", false))
}

func TestIsPathAllowed_TraversalAttempts(t *testing.T) {
	assert.False(t, IsPathAllowed("/DATA/../etc", false))
	assert.False(t, IsPathAllowed("/DATA/../../etc/passwd", false))
	assert.False(t, IsPathAllowed("/mnt/../root", false))
}

func TestIsPathAllowed_EmptyPath(t *testing.T) {
	assert.False(t, IsPathAllowed("", false))
	assert.False(t, IsPathAllowed("", true))
}
