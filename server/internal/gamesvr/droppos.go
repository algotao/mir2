package gamesvr

import (
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// maxDropStackPerCell 是原版 GetDropPosition 里的那个 8：
// 一圈都没找到空格时，只有当"物品最少的那格"少于 8 件才往上叠，否则落原地。
const maxDropStackPerCell = 8

// 掉落散落范围（原版各处 DropItemDown 的 nScatterRange 实参）：
//
//	怪物死亡掉落   3   ObjBase.pas:20641（`dropwide := 3`，非玩家分支）
//	玩家死亡掉包裹 2   ObjBase.pas:20570（`dropwide := 2`）
//	玩家死亡掉装备 2   ObjBase.pas:15533（`DropItemDown(@m_UseItems[i], 2, True, …)`）
//	玩家主动扔道具 1   ObjBase.pas:16265 / 6643（`DropItemDown(UserItem, 1, False, …)`）
//
// ⚠️ 本项目文档此前写的是"装备 3 格、包裹 7 格（DropItemRage 上限）"——
// 那是 **OpenMir2**（C# 移植）的常量，官方 Delphi 1.76 的实参是上面这四个
// （第 4 种我们还没有：`CM_DROPITEM` 未实现，所以没有落点）。
const (
	dropRangeMonsterDie = 3
	dropRangePlayerDie  = 2
	// dropRangePlayerThrow 是**玩家主动扔物品**（`CM_DROPITEM`）的散落范围：
	// `DropItemDown(UserItem, 1, False, nil, Self)`（ObjBase.pas:16265）。
	// ⚠️ 这条路径 2026-10-06 之前根本没实现（客户端发上来没人处理），见 docs/gap-audit.md。
	dropRangePlayerThrow = 1
)

// dropPosition 返回一次掉落的落点，逐句对照原版 `TBaseObject.GetDropPosition`
// （ObjBase.pas:1534-1584）。
//
// ⚠️ 它**不是**随机散落，而是**确定性的螺旋扫描**——"散落"这个名字会误导人：
//
//	for i := 1 to nRange do          // 一圈一圈向外（每圈重扫整个正方形）
//	  for ii := -i to i do           // dy
//	    for III := -i to i do        // dx
//	      候选 := (orgX+III, orgY+ii)
//	      if 可落(bo2C) and 该格无地面物品 then 选中并立即结束
//	      if 可落(bo2C) and 该格有地面物品 then 记下"物品最少的那格"
//	一圈都没选中时：
//	  最少那格的物品数 < 8 ⇒ 落那格（叠上去）
//	  否则               ⇒ 落原地
//
// 四个必须注意的点：
//
//  1. **原点也在扫描范围内**（i=1、ii=0、III=0 那格），但它在同类格里排第 5
//     —— 顺序是 (-1,-1)、(0,-1)、(1,-1)、(-1,0)、**(0,0)** —— 所以只要
//     (orgX-1, orgY-1) 之类更早的格子可落且空着，就不会落原地。
//  2. `bo2C`（原版 Envir.pas:695-730 的 `GetItemEx`）要求该格**可通行**、
//     **没有城门**、**没有活着的对象站着**；死物不算 ⇒ 尸体上可以叠。
//     我们没有独立的"城门对象"（城堡门/城墙本身就是 Monster，见 guard.go 的
//     分类）⇒ "没有活着的怪物"这一条自动把门盖住了。
//  3. 原版**每件物品各算一次落点**（`DropItemDown` 逐件调用），所以同一次死亡掉
//     的多件东西会自己散开：后面的那件能看到前面那件已经占了格子。
//     调用方也**必须逐件调用**，不能只算一次坐标复用。
//  4. ⚠️ 我们的两个空间索引是**全局**的（不分地图），而原版的 ObjList 挂在
//     "地图的格子上" ⇒ 这里必须逐格比对**地图 + 坐标**，否则别的地图上
//     同坐标的玩家会凭空挡住落点。
//
// 关于 8 的一处实现歧义：反编译版里 `GetItemEx` 的出参 `nCount` 没被赋值
// （找到物品就直接 Exit），于是 `n24 > nItemCount` 退化成"记住第一个有物品的格子"。
// 但 `n24 := 999` 的初值与 `< 8` 的比较都指向"计数版"语义 ⇒ 按**计数版**实现。
// 两种读法只在"某格已有物品、都没到 8 件、且各格件数不同"时才分叉。
//
// ⚠️ 本函数自己加 `s.mu`（要读地面物品与两个空间索引）⇒ **调用方不能持锁**。
// ignoreID 是"刚死掉、还没从索引里摘掉的那个对象"（原版靠 `m_boDeath` 排除它）。
func (s *Server) dropPosition(m *world.Map, orgX, orgY, nRange int, ignoreID uint32) (int, int) {
	if m == nil || nRange <= 0 {
		return orgX, orgY
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	bestX, bestY, bestCount := orgX, orgY, maxDropStackPerCell+1
	for i := 1; i <= nRange; i++ {
		for dy := -i; dy <= i; dy++ {
			for dx := -i; dx <= i; dx++ {
				x, y := orgX+dx, orgY+dy
				if !s.canDropAt(m, x, y, ignoreID) {
					continue
				}
				n := s.groundCountAt(m, x, y)
				if n == 0 {
					return x, y
				}
				if n < bestCount {
					bestCount, bestX, bestY = n, x, y
				}
			}
		}
	}
	if bestCount < maxDropStackPerCell {
		return bestX, bestY
	}
	return orgX, orgY
}

// canDropAt 对应原版 `GetItemEx` 里的 `bo2C`：该格可通行、没有城门、没有活着的对象。
//
// ⚠️ 调用方必须已持 `s.mu`（`dropPosition` 已持）。
func (s *Server) canDropAt(m *world.Map, x, y int, ignoreID uint32) bool {
	if !m.CanWalk(x, y) {
		return false
	}
	// ⚠️ 索引是**全局**的（按 32×32 chunk），所以既要逐格比对坐标，也要比对地图。
	for _, o := range s.world.index.InRange(x, y, 0) {
		p, ok := o.(*Player)
		if !ok || p.Obj == nil || p.Obj.MapRef() != m || p.Obj.PosX() != x || p.Obj.PosY() != y {
			continue
		}
		if p.Obj.ID == ignoreID {
			continue // 刚死的那个：原版看 m_boDeath，不算阻挡
		}
		return false
	}
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		mon, ok := o.(*entity.Monster)
		if !ok || mon.Object == nil || mon.Object.MapRef() != m || mon.PosX() != x || mon.PosY() != y {
			continue
		}
		if mon.ID == ignoreID {
			continue
		}
		if mon.IsDead() {
			continue // 尸体不阻挡（原版 `if not BaseObject.m_boDeath then bo2C := False`）
		}
		return false
	}
	return true
}

// groundCountAt 返回该格已有的地面物品件数（原版 `GetItemEx` 的 `nCount`）。
//
// ⚠️ 调用方必须已持 `s.mu`。
func (s *Server) groundCountAt(m *world.Map, x, y int) int {
	n := 0
	for _, gi := range s.world.ground {
		if gi.Map == m && gi.X == x && gi.Y == y {
			n++
		}
	}
	return n
}
