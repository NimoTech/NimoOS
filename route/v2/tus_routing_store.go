package v2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tus/tusd/v2/pkg/filestore"
	"github.com/tus/tusd/v2/pkg/handler"
)

// stagingIDSep separates the hex-encoded volume root from the sub-store's own
// generated id in a routed upload ID, e.g. "2f6d656469612f524149445f30~<hex>".
// '~' is an unreserved (URL-safe) character per RFC 3986 and tusd's own
// upload-id validation (see handler.reValidUploadId), so no escaping is needed.
const stagingIDSep = "~"

// encodeStagingID encodes a volume root and a sub-store id into a single,
// URL-safe upload ID that routingStore can decode back to the same root
// without needing any external state (works across a service restart).
func encodeStagingID(root, sub string) string {
	return hex.EncodeToString([]byte(root)) + stagingIDSep + sub
}

// decodeStagingID reverses encodeStagingID. ok is false for IDs that carry no
// "~" separator — i.e. legacy, pre-routing plain IDs — which callers must
// route to the legacy staging dir for backwards/in-flight-upload compatibility.
func decodeStagingID(id string) (root, sub string, ok bool) {
	i := strings.Index(id, stagingIDSep)
	if i < 0 {
		return "", "", false
	}
	rootBytes, err := hex.DecodeString(id[:i])
	if err != nil {
		return "", "", false
	}
	return string(rootBytes), id[i+1:], true
}

// newSubID returns a random, 128-bit hex id, matching the strength (though not
// the package) of tusd's own internal/uid.Uid() generator (which we cannot
// import — it's unexported from github.com/tus/tusd/v2).
func newSubID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate upload id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// probeFileStore is a zero-value filestore.FileStore used only to reach its
// As*Upload methods, all of which are pure type assertions on the passed
// handler.Upload and never touch the receiver's Path — see
// github.com/tus/tusd/v2/pkg/filestore.FileStore.AsTerminatableUpload et al.
// This lets routingStore forward these calls without tracking which
// per-volume store produced a given handler.Upload.
var probeFileStore = filestore.FileStore{}

// routingStore is a tusd handler.DataStore that fans out to one
// filestore.FileStore per target volume, so that tus upload staging lands on
// the same filesystem as the upload's final destination instead of always on
// /DATA (which caused spurious 413s and cross-device copies for large
// uploads to other volumes, e.g. a RAID array under /media).
//
// Routing works without any shared/persisted state: the resolved volume root
// is hex-encoded directly into the upload ID (see encodeStagingID), so
// GetUpload can route a request straight to the right per-volume store even
// after a service restart. Uploads that resolve to the legacy root (/DATA
// itself, or anything resolveStagingRoot falls back on) keep the exact
// pre-existing plain-ID, single-directory behavior.
type routingStore struct {
	mu         sync.Mutex
	stores     map[string]filestore.FileStore // keyed by resolved root
	mountsFn   func() []MountEntry
	legacyRoot string // resolveStagingRoot's fallback sentinel, see legacyStagingRoot
	legacyDir  string // actual on-disk dir for legacyRoot uploads
}

// newRoutingStore creates a routingStore. legacyDir is the pre-existing
// staging directory (common.FileUploadStagingDir in production) used for the
// legacy root and for any pre-routing, no-prefix upload IDs. mountsFn supplies
// a mount-table snapshot on demand (liveMounts in production, a fixed slice
// in tests).
func newRoutingStore(legacyDir string, mountsFn func() []MountEntry) *routingStore {
	return &routingStore{
		stores:     make(map[string]filestore.FileStore),
		mountsFn:   mountsFn,
		legacyRoot: legacyStagingRoot,
		legacyDir:  legacyDir,
	}
}

// dirForRoot maps a resolved root to its actual on-disk staging directory.
// The legacy root is special-cased to the injected legacyDir rather than the
// generic <root>/.system_data/file-tus-staging formula, both so that
// production wiring reuses the exact pre-existing directory unchanged, and so
// that tests never have to touch the real /DATA on whatever machine they run on.
func (s *routingStore) dirForRoot(root string) string {
	if root == s.legacyRoot {
		return s.legacyDir
	}
	return stagingDirForRoot(root)
}

// storeForRoot returns the filestore.FileStore for root, lazily creating its
// staging directory (0700) on first use.
func (s *routingStore) storeForRoot(root string) (filestore.FileStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fs, ok := s.stores[root]; ok {
		return fs, nil
	}
	dir := s.dirForRoot(root)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return filestore.FileStore{}, fmt.Errorf("mkdir staging dir %s: %w", dir, err)
	}
	fs := filestore.New(dir)
	s.stores[root] = fs
	return fs, nil
}

// NewUpload resolves the target volume from info.MetaData["targetPath"] and
// creates the upload in that volume's own staging directory. Uploads that
// fall back to the legacy root keep a plain, unprefixed ID (matching the
// pre-existing filestore-only behavior exactly); everything else gets the
// volume root encoded into its ID so GetUpload can route it later.
func (s *routingStore) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	targetPath := info.MetaData["targetPath"]
	root, _ := resolveStagingRoot(targetPath, s.mountsFn())

	fs, err := s.storeForRoot(root)
	if err != nil {
		return nil, err
	}

	if info.ID == "" && root != s.legacyRoot {
		sub, serr := newSubID()
		if serr != nil {
			return nil, serr
		}
		info.ID = encodeStagingID(root, sub)
	}

	return fs.NewUpload(ctx, info)
}

// GetUpload decodes the volume root from id's prefix and routes to that
// volume's store. IDs without a recognizable prefix (created before routing
// existed, or created for the legacy root) are routed to the legacy dir —
// this is what keeps in-flight/resumable uploads working across the deploy
// that introduces per-volume staging.
func (s *routingStore) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	root, _, ok := decodeStagingID(id)
	if !ok {
		root = s.legacyRoot
	}
	fs, err := s.storeForRoot(root)
	if err != nil {
		return nil, err
	}
	return fs.GetUpload(ctx, id)
}

// The As*Upload methods below satisfy tusd's optional extension-store
// interfaces (handler.TerminaterDataStore, ConcaterDataStore,
// LengthDeferrerDataStore, ContentServerDataStore). They forward through
// probeFileStore since these methods are pure type assertions that do not
// depend on which per-volume filestore.FileStore produced the handler.Upload.

func (s *routingStore) AsTerminatableUpload(upload handler.Upload) handler.TerminatableUpload {
	return probeFileStore.AsTerminatableUpload(upload)
}

func (s *routingStore) AsConcatableUpload(upload handler.Upload) handler.ConcatableUpload {
	return probeFileStore.AsConcatableUpload(upload)
}

func (s *routingStore) AsLengthDeclarableUpload(upload handler.Upload) handler.LengthDeclarableUpload {
	return probeFileStore.AsLengthDeclarableUpload(upload)
}

func (s *routingStore) AsServableUpload(upload handler.Upload) handler.ServableUpload {
	return probeFileStore.AsServableUpload(upload)
}
