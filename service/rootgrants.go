/*
 * @Description: RootGrantRepo — read/write implementation for the
 * o_root_grants authorization table. Reused across the search-authorization
 * chain (Wiki reconciliation / virtual root seed / search-side reads of the
 * enabled list).
 */
package service

import (
	"time"

	"github.com/NimoTech/NimoOS/service/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RootGrantRepo is the read/write interface for the o_root_grants table.
type RootGrantRepo interface {
	// UpsertGrant upserts one row keyed on rootID; UpdatedAt takes the current time.
	UpsertGrant(rootID, path string, enabled bool, source string) error
	// DeleteGrant deletes one row by rootID.
	DeleteGrant(rootID string) error
	// EnabledRootIDs returns all root_id with enabled=true (regardless of source, including virtual).
	EnabledRootIDs() ([]string, error)
	// EnabledRoots returns every enabled grant with its path, so consumers
	// that scope by filesystem path (Search's filename index) can map ids to
	// paths without a second lookup. Virtual roots carry an empty path.
	EnabledRoots() ([]model.RootGrant, error)
	// ReconcileWiki fully syncs the source="wiki" rows against the input: adds/updates
	// rows present in the list, deletes rows not in the list; never touches source="virtual" rows.
	ReconcileWiki(grants []model.RootGrant) error
	// SeedVirtual idempotently registers a virtual root (source="virtual", enabled=true, path="").
	SeedVirtual(rootID string) error
}

type rootGrantStore struct {
	db *gorm.DB
}

// NewRootGrantRepo constructs a RootGrantRepo implementation.
func NewRootGrantRepo(db *gorm.DB) RootGrantRepo {
	return &rootGrantStore{db: db}
}

// upsert is the shared internal implementation: on a root_id primary-key
// conflict, it updates the specified columns.
func (s *rootGrantStore) upsert(row model.RootGrant) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "root_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"path", "enabled", "source", "updated_at"}),
	}).Create(&row).Error
}

func (s *rootGrantStore) UpsertGrant(rootID, path string, enabled bool, source string) error {
	return s.upsert(model.RootGrant{
		RootID:    rootID,
		Path:      path,
		Enabled:   enabled,
		Source:    source,
		UpdatedAt: time.Now().UnixMilli(),
	})
}

func (s *rootGrantStore) DeleteGrant(rootID string) error {
	return s.db.Where("root_id = ?", rootID).Delete(&model.RootGrant{}).Error
}

func (s *rootGrantStore) EnabledRoots() ([]model.RootGrant, error) {
	rows := []model.RootGrant{}
	err := s.db.Where("enabled = ?", true).Order("root_id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *rootGrantStore) EnabledRootIDs() ([]string, error) {
	// Explicitly start from an initialized empty slice (rather than the nil
	// of `var ids []string`) to harden the contract: an empty table must
	// also return []string{}, so callers (e.g. the SearchRoots handler)
	// JSON-serialize it as [] rather than null. Don't rely on gorm Pluck's
	// internal initialization behavior for the target slice.
	ids := []string{}
	err := s.db.Model(&model.RootGrant{}).Where("enabled = ?", true).Pluck("root_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ReconcileWiki fully reconciles the source="wiki" rows in a single
// transaction: first deletes wiki rows not present in the input list, then
// upserts each input row one by one (source is hardcoded to "wiki",
// ignoring any other value the input might carry). If the input is empty,
// all source="wiki" rows are deleted. source="virtual" is never touched.
//
// Namespace assumption (implicit, no defensive code, YAGNI): the upsert
// loop below does not check whether a root_id in the input grants collides
// with a virtual root registered via SeedVirtual (e.g. "photos"). This
// relies on the fact that Wiki-side root_id generation and the virtual-root
// namespace are, in practice, disjoint: Wiki's root_id is the hex encoding
// of a crypto/rand-generated 16-byte random number (128 bits of randomness),
// while virtual root ids like "photos" are hardcoded short constant
// strings — the two differ enough in length and character distribution
// that a Wiki-supplied random root_id happening to equal some virtual root
// id can be ignored. If virtual roots ever switch to a naming scheme
// isomorphic to Wiki's root_id (e.g. also 16-byte hex), this assumption no
// longer holds and an explicit namespace check must be added here.
func (s *rootGrantStore) ReconcileWiki(grants []model.RootGrant) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		ids := make([]string, 0, len(grants))
		for _, g := range grants {
			ids = append(ids, g.RootID)
		}

		if len(ids) == 0 {
			if err := tx.Where("source = ?", "wiki").Delete(&model.RootGrant{}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Where("source = ? AND root_id NOT IN ?", "wiki", ids).Delete(&model.RootGrant{}).Error; err != nil {
				return err
			}
		}

		now := time.Now().UnixMilli()
		for _, g := range grants {
			row := model.RootGrant{
				RootID:    g.RootID,
				Path:      g.Path,
				Enabled:   g.Enabled,
				Source:    "wiki",
				UpdatedAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "root_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"path", "enabled", "source", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *rootGrantStore) SeedVirtual(rootID string) error {
	return s.upsert(model.RootGrant{
		RootID:    rootID,
		Path:      "",
		Enabled:   true,
		Source:    "virtual",
		UpdatedAt: time.Now().UnixMilli(),
	})
}
