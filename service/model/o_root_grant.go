/*
 * @Description: 根目录检索授权表——记录哪些 root_id 被授权参与检索(RAG/文件名/图片检索),
 * 以及它们各自的来源(wiki 对账产生 or 虚拟根手动 seed)。
 */
package model

// RootGrant 对应表 o_root_grants,一行代表一个 root 的授权状态。
// RootID 为主键:
//   - source="wiki" 的行由 Wiki 侧节点树对账(ReconcileWiki)全量同步产生;
//   - source="virtual" 的行由虚拟根(如 photos)通过 SeedVirtual 手动登记,
//     ReconcileWiki 绝不触碰这些行。
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
