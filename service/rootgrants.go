/*
 * @Description: RootGrantRepo——o_root_grants 授权表的读写实现。供检索授权链路
 * (Wiki 对账 / 虚拟根 seed / 检索侧读取 enabled 列表)复用。
 */
package service

import (
	"time"

	"github.com/NimoTech/NimoOS/service/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RootGrantRepo 是 o_root_grants 表的读写接口。
type RootGrantRepo interface {
	// UpsertGrant 按 rootID 主键 upsert 一行,UpdatedAt 取当前时间。
	UpsertGrant(rootID, path string, enabled bool, source string) error
	// DeleteGrant 按 rootID 删除一行。
	DeleteGrant(rootID string) error
	// EnabledRootIDs 返回所有 enabled=true 的 root_id(不区分 source,含 virtual)。
	EnabledRootIDs() ([]string, error)
	// ReconcileWiki 以入参为准全量同步 source="wiki" 的行:新增/更新在列表里的、
	// 删除不在列表里的;绝不触碰 source="virtual" 的行。
	ReconcileWiki(grants []model.RootGrant) error
	// SeedVirtual 幂等地登记一个虚拟根(source="virtual", enabled=true, path="")。
	SeedVirtual(rootID string) error
}

type rootGrantStore struct {
	db *gorm.DB
}

// NewRootGrantRepo 构造 RootGrantRepo 实现。
func NewRootGrantRepo(db *gorm.DB) RootGrantRepo {
	return &rootGrantStore{db: db}
}

// upsert 是内部公共实现:按 root_id 主键冲突时更新指定列。
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

func (s *rootGrantStore) EnabledRootIDs() ([]string, error) {
	// 显式以初始化过的空切片起始(而非 var ids []string 的 nil),硬化契约:
	// 空表也要返回 []string{},调用方(如 SearchRoots handler)JSON 序列化后
	// 得到 [] 而不是 null。不依赖 gorm Pluck 对目标切片的内部初始化行为。
	ids := []string{}
	err := s.db.Model(&model.RootGrant{}).Where("enabled = ?", true).Pluck("root_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ReconcileWiki 在一个事务里全量对账 source="wiki" 的行:先删掉不在入参列表里的
// wiki 行,再逐条 upsert 入参(source 固定写死为 "wiki",忽略入参里可能带的其它
// 值)。入参为空时,删除所有 source="wiki" 的行。全程不涉及 source="virtual"。
//
// 命名空间假设(隐含、未做防御代码,YAGNI):下面的 upsert 循环不检查入参
// grants 里的 root_id 是否撞上了 SeedVirtual 登记的虚拟根(如 "photos")。
// 这依赖 Wiki 侧 root_id 的生成方式与虚拟根命名空间事实不相交:Wiki 的
// root_id 取自 crypto/rand 生成的 16 字节随机数的 hex 编码(128 位随机空间),
// 而 "photos" 这类虚拟根 id 是硬编码的短常量字符串,二者长度和字符分布都
// 对不上,可忽略 Wiki 侧传入一个恰好等于某虚拟根 id 的随机 root_id 的可能性。
// 若未来虚拟根改用与 Wiki root_id 同构的命名(比如也上 16 字节 hex),
// 这条假设就不再成立,需要在这里补显式的命名空间校验。
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
