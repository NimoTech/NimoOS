/*
 * @Description: Root-directory search authorization table — records which
 * root_id are authorized to participate in search (RAG/filename/image
 * search), along with each one's source (produced by wiki reconciliation
 * or manually seeded as a virtual root).
 */
package model

// RootGrant corresponds to table o_root_grants; one row represents one
// root's authorization state. RootID is the primary key:
//   - source="wiki" rows are produced by full sync from the Wiki-side node
//     tree reconciliation (ReconcileWiki);
//   - source="virtual" rows are manually registered by a virtual root
//     (e.g. photos) via SeedVirtual; ReconcileWiki never touches these rows.
type RootGrant struct {
	RootID    string `gorm:"column:root_id;primaryKey" json:"root_id"`
	Path      string `gorm:"column:path" json:"path"`
	Enabled   bool   `gorm:"column:enabled" json:"enabled"`
	Source    string `gorm:"column:source" json:"source"`
	UpdatedAt int64  `gorm:"column:updated_at" json:"updated_at"`
}

func (RootGrant) TableName() string {
	return "o_root_grants"
}
