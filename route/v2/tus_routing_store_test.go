package v2

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tus/tusd/v2/pkg/handler"
)

func TestStagingIDEncodeDecodeRoundTrip(t *testing.T) {
	root := "/media/RAID_0"
	sub := "deadbeef0123"
	id := encodeStagingID(root, sub)

	gotRoot, gotSub, ok := decodeStagingID(id)
	if !ok {
		t.Fatalf("decodeStagingID(%q) ok=false, want true", id)
	}
	if gotRoot != root || gotSub != sub {
		t.Fatalf("decodeStagingID(%q) = (%q, %q), want (%q, %q)", id, gotRoot, gotSub, root, sub)
	}
	// Encoded ID must be URL-safe (hex + '~' only).
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef~", r) {
			t.Fatalf("id %q contains non URL-safe rune %q", id, r)
		}
	}
}

func TestDecodeStagingIDNoPrefixIsLegacy(t *testing.T) {
	_, _, ok := decodeStagingID("plainhexid1234")
	if ok {
		t.Fatal("plain (no '~') id must decode ok=false so callers route it to the legacy dir")
	}
}

// NewUpload must place the staged file under the resolved volume's own
// staging dir, keyed off targetPath, and encode that volume root into the ID.
func TestRoutingStoreNewUploadRoutesByTargetPath(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	legacyDir := t.TempDir()

	mounts := []MountEntry{
		{Mountpoint: rootA, FSType: "ext4"},
		{Mountpoint: rootB, FSType: "btrfs"},
	}
	rs := newRoutingStore(legacyDir, func() []MountEntry { return mounts })

	info := handler.FileInfo{
		Size:     5,
		MetaData: map[string]string{"targetPath": filepath.Join(rootA, "sub", "file.bin")},
	}
	up, err := rs.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	got, err := up.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	decodedRoot, _, ok := decodeStagingID(got.ID)
	if !ok || decodedRoot != rootA {
		t.Fatalf("id %q must decode to root %q, got root=%q ok=%v", got.ID, rootA, decodedRoot, ok)
	}

	wantDir := stagingDirForRoot(rootA)
	if !strings.HasPrefix(got.Storage["Path"], wantDir) {
		t.Fatalf("staged file path = %q, want under %q", got.Storage["Path"], wantDir)
	}
	if _, err := os.Stat(got.Storage["Path"]); err != nil {
		t.Fatalf("staged file not created on disk: %v", err)
	}

	// rootB's staging dir must not have been touched by an upload targeting rootA.
	if _, err := os.Stat(stagingDirForRoot(rootB)); !os.IsNotExist(err) {
		t.Fatalf("rootB staging dir should not exist yet, stat err = %v", err)
	}
}

// Uploads whose targetPath does not resolve to an eligible local volume
// (fallback case) must land in the legacy dir with a plain, unprefixed ID —
// preserving the pre-existing on-disk layout and ID format for /DATA exactly.
func TestRoutingStoreNewUploadFallsBackToLegacyDirWithPlainID(t *testing.T) {
	legacyDir := t.TempDir()
	rs := newRoutingStore(legacyDir, func() []MountEntry { return nil }) // nothing matches -> fallback

	info := handler.FileInfo{
		Size:     3,
		MetaData: map[string]string{"targetPath": "/DATA/somewhere/f.bin"},
	}
	up, err := rs.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	got, err := up.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if strings.Contains(got.ID, "~") {
		t.Fatalf("legacy/DATA upload id must not carry a volume prefix, got %q", got.ID)
	}
	if !strings.HasPrefix(got.Storage["Path"], legacyDir) {
		t.Fatalf("expected file under legacy dir %s, got %s", legacyDir, got.Storage["Path"])
	}
}

// GetUpload must decode the prefix and route to the right volume's store, and
// must work from a fresh routingStore instance (no reliance on NewUpload's
// in-memory cache) since a service restart loses that cache.
func TestRoutingStoreGetUploadRoutesByPrefix(t *testing.T) {
	rootA := t.TempDir()
	legacyDir := t.TempDir()
	mounts := []MountEntry{{Mountpoint: rootA, FSType: "ext4"}}
	mountsFn := func() []MountEntry { return mounts }

	rs1 := newRoutingStore(legacyDir, mountsFn)
	info := handler.FileInfo{
		Size:     7,
		MetaData: map[string]string{"targetPath": filepath.Join(rootA, "f.bin")},
	}
	up, err := rs1.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	created, _ := up.GetInfo(context.Background())

	rs2 := newRoutingStore(legacyDir, mountsFn) // fresh instance, no shared state
	fetched, err := rs2.GetUpload(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	fetchedInfo, err := fetched.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if fetchedInfo.Storage["Path"] != created.Storage["Path"] {
		t.Fatalf("fetched path %q != created path %q", fetchedInfo.Storage["Path"], created.Storage["Path"])
	}
}

// A pre-existing, no-prefix upload ID (created before this change shipped)
// must still resolve via GetUpload — routed straight to the legacy dir.
func TestRoutingStoreGetUploadLegacyNoPrefixID(t *testing.T) {
	legacyDir := t.TempDir()
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-existing plain-ID upload written directly by the old,
	// un-routed filestore (no '~' prefix).
	plainID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(filepath.Join(legacyDir, plainID), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	infoJSON := `{"ID":"` + plainID + `","Size":5,"Offset":5,"MetaData":{},"Storage":{"Type":"filestore","Path":"` +
		filepath.Join(legacyDir, plainID) + `","InfoPath":"` + filepath.Join(legacyDir, plainID+".info") + `"}}`
	if err := os.WriteFile(filepath.Join(legacyDir, plainID+".info"), []byte(infoJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rs := newRoutingStore(legacyDir, func() []MountEntry { return nil })
	up, err := rs.GetUpload(context.Background(), plainID)
	if err != nil {
		t.Fatalf("GetUpload legacy id: %v", err)
	}
	got, err := up.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if got.Size != 5 || got.Offset != 5 {
		t.Fatalf("unexpected info: %+v", got)
	}
}

// AsTerminatableUpload must forward correctly regardless of which volume the
// upload was routed to, so DELETE (cancel) works end to end.
func TestRoutingStoreAsTerminatableUpload(t *testing.T) {
	rootA := t.TempDir()
	legacyDir := t.TempDir()
	mounts := []MountEntry{{Mountpoint: rootA, FSType: "ext4"}}
	rs := newRoutingStore(legacyDir, func() []MountEntry { return mounts })

	info := handler.FileInfo{
		Size:     4,
		MetaData: map[string]string{"targetPath": filepath.Join(rootA, "f.bin")},
	}
	up, err := rs.NewUpload(context.Background(), info)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	created, _ := up.GetInfo(context.Background())

	term := rs.AsTerminatableUpload(up)
	if term == nil {
		t.Fatal("AsTerminatableUpload returned nil")
	}
	if err := term.Terminate(context.Background()); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if _, err := os.Stat(created.Storage["Path"]); !os.IsNotExist(err) {
		t.Fatalf("staged file should be removed after Terminate, stat err = %v", err)
	}
}
