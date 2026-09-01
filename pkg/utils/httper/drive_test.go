package httper

import (
	"encoding/json"
	"testing"
)

// A locked OneDrive Personal Vault rejects every Graph listing, so onedrive
// mounts must carry an exclude filter for it; every other remote type must
// keep mounting with no filter at all.
func TestVaultExcludeFilter(t *testing.T) {
	got := vaultExcludeFilter("onedrive")
	if got == "" {
		t.Fatal("onedrive remotes must get a Personal Vault exclude filter")
	}
	var f struct {
		ExcludeRule []string `json:"ExcludeRule"`
	}
	if err := json.Unmarshal([]byte(got), &f); err != nil {
		t.Fatalf("filter is not valid JSON: %v — %s", err, got)
	}
	if len(f.ExcludeRule) == 0 {
		t.Fatalf("filter has no ExcludeRule: %s", got)
	}
	found := false
	for _, r := range f.ExcludeRule {
		if r == "Personal Vault/**" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ExcludeRule missing \"Personal Vault/**\": %s", got)
	}

	for _, typ := range []string{"dropbox", "drive", "google_drive", "", "smb"} {
		if got := vaultExcludeFilter(typ); got != "" {
			t.Fatalf("remote type %q must not get a filter, got %s", typ, got)
		}
	}
}
