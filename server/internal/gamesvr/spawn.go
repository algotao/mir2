package gamesvr

import (
	"log"
	"math/rand/v2"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// NpcIDBase 是 NPC 的 ActorId 起点。
//
// 与怪物（1_000_000 起）分开，便于一眼区分，也避免 ID 撞车。
// spawnNPCs 在某地图生成属于该地图的 NPC。
//
// NPC 复用 entity.Monster 以直接借用实体/视野/广播机制，
// 但标记 IsNPC：不游荡、不攻击、不掉落。
func (s *Server) spawnNPCs(mapID string) {
	s.mu.Lock()
	// ⚠️ `npcState.spawned` 在最小测试服里是 nil（生产路径由启动流程建好）
	// ⇒ 直接写会 panic（`assignment to entry in nil map`）。
	if s.npc.spawned == nil {
		s.npc.spawned = make(map[string]bool)
	}
	if s.npc.spawned[mapID] {
		s.mu.Unlock()
		return
	}
	s.npc.spawned[mapID] = true
	s.mu.Unlock()

	mp, err := s.world.maps.Get(mapID)
	if err != nil {
		log.Printf("WARN: NPC 地图 %s 加载失败: %v", mapID, err)
		return
	}
	list := data.NPCsInMap(s.npc.defs, mapID)
	n := 0
	for _, np := range list {
		if !mp.InBounds(np.X, np.Y) {
			log.Printf("WARN: NPC %s（地图 %s）坐标 (%d,%d) 越界，未生成", np.Name, mapID, np.X, np.Y)
			continue
		}
		// NPC 没有怪物模板，现场合成一个：不会死、不会动、不会攻击
		info := &data.MonsterInfo{
			Name:        np.Name,
			RaceImg:     uint16(np.RaceImg),
			Appr:        uint16(np.Body),
			Level:       1,
			HP:          999999,
			WalkSpeed:   0,
			AttackSpeed: 0,
		}
		npc := entity.NewMonster(proto.NpcIDBase+s.npc.seq.Add(1), info, mp, np.X, np.Y)
		npc.IsNPC = true
		// 属沙城的 NPC：在这里交易要抽城堡税（原版 m_boCastle）
		npc.CastleNPC = np.Castle

		s.mu.Lock()
		s.world.monsters[npc.ID] = npc
		s.world.monsterIdx.Add(npc)
		s.mu.Unlock()
		n++
	}
	if n > 0 {
		log.Printf("地图 %s 生成 %d 个 NPC", mapID, n)
	}
}

func (s *Server) activateSpawnMap(mapID string) {
	if mapID == "" {
		return
	}
	s.mu.Lock()
	if s.world.activeSpawnMaps == nil {
		s.world.activeSpawnMaps = make(map[string]bool)
	}
	newlyActive := !s.world.activeSpawnMaps[mapID]
	s.world.activeSpawnMaps[mapID] = true
	s.mu.Unlock()
	if newlyActive {
		s.refillSpawns()
	}
}

func (s *Server) refillSpawns() {
	var spawned []*entity.Monster

	s.mu.Lock()
	for _, sp := range s.world.spawns {
		if s.world.activeSpawnMaps != nil && !s.world.activeSpawnMaps[sp.MapName] {
			continue
		}
		// 用空间索引查局部候选，避免全量地图初始化后每个点都扫描全部怪物。
		alive := 0
		for _, candidate := range s.world.monsterIdx.InRange(sp.X, sp.Y, sp.Range+2) {
			m, ok := candidate.(*entity.Monster)
			if !ok || m.IsNPC || m.MasterID != 0 || m.IsDead() || m.Name != sp.MonsterName ||
				m.MapRef() == nil || m.MapRef().Name != sp.MapName {
				continue
			}
			if m.Distance(sp.X, sp.Y) <= sp.Range+2 {
				alive++
			}
		}
		for i := alive; i < sp.Count; i++ {
			if m := s.spawnOne(sp); m != nil {
				spawned = append(spawned, m)
			}
		}
	}
	s.mu.Unlock()

	// ⚠️ 必须主动广播：updateVision 只在**玩家移动**或进游戏时触发，
	// 若玩家站着不动，新刷出的怪永远不会出现在他视野里（表现为"怪凭空消失/出现"）。
	for _, m := range spawned {
		s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(p *Player) {
			s.sendMonsterAppear(p, m)
		})
	}
}

// spawnOne 在指定刷怪点生成一只怪。**调用方持锁**；返回 nil 表示未生成。
func (s *Server) spawnOne(sp entity.SpawnPoint) *entity.Monster {
	if sp.Range < 0 {
		return nil
	}
	info := s.data.tables.Monsters.GetByName(sp.MonsterName)
	if info == nil {
		return nil
	}
	// 按需加载该刷怪点所属地图；加载失败就跳过这个点，
	// 而不是回退到默认地图——否则怪会被刷到另一张图上。
	m, err := s.world.maps.Get(sp.MapName)
	if err != nil {
		log.Printf("刷怪点地图 %s 加载失败: %v", sp.MapName, err)
		return nil
	}
	// 在范围内找一个可通行点
	x, y := sp.X, sp.Y
	for i := 0; i < 10; i++ {
		cx := sp.X + rand.IntN(sp.Range*2+1) - sp.Range
		cy := sp.Y + rand.IntN(sp.Range*2+1) - sp.Range
		// 同格检查与 gmSpawn 一致：原版每格只容一个对象。
		if s.cellFreeLocked(m, cx, cy) {
			x, y = cx, cy
			break
		}
	}
	if !s.cellFreeLocked(m, x, y) {
		var ok bool
		x, y, ok = s.freeCellNearLocked(m, sp.X, sp.Y, sp.Range, -1, -1)
		if !ok {
			return nil
		}
	}

	mon := entity.NewMonster(proto.MonsterIDBase+s.world.monsterSeq.Add(1), info, m, x, y)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)
	return mon
}

// spawnGroundItem 把一件玩家物品放到地图上并广播 SM_ADDITEM。
// handleDropItem 处理 CM_DROPITEM：把背包里的物品丢到地上。
//
// 原版 `ObjBase.pas:4679` 的 `CM_DROPITEM` ⇒ `ClientDropItem(sMsg, nParam1)`；
// 客户端发的是 `MakeDefaultMsg(CM_DROPITEM, itemserverindex, 0, 0, 0)`
// （`Client/ClMain.pas:3042` 的 `SendDropItem`）⇒ **Recog = 物品的 MakeIndex**，
// 包体是物品名（可能是玩家自定义名）。成功后回 `SM_DROPITEM_SUCCESS`
// （`Recog = MakeIndex`），客户端据此把它从背包里拿掉。
//
// 落点：`DropItemDown(UserItem, 1, False, nil, Self)`（`:16265`）⇒ 范围 **1**。
// 地图标记 `NOTHROWITEM`（`:16202`/`:16233` 的 `if not m_boCanDrop or …NOTHROWITEM`）
// 时整段禁止。⚠️ 原版还有 `m_boCanDrop`（脚本开关，我们没建模）⇒ 只实现了地图那半。
func (s *Server) handleDropItem(c net.Conn, p *Player, m wire.Packet) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Obj == nil {
		return
	}
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoThrowItem {
		s.sysMsg(c, "此地图不允许丢弃物品")
		return
	}
	data0 := p.Char.Data
	idx := -1
	for i, it := range data0.BagItems {
		if it == nil || it.Index == 0 || int32(it.MakeIndex) != m.Head.Recog {
			continue
		}
		// 客户端带了名字就一并核对（原版 `ClientDropItem` 也按名字找）
		if m.Body != "" {
			tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
			if tmpl == nil || tmpl.Name != m.Body {
				continue
			}
		}
		idx = i
		break
	}
	if idx < 0 {
		return // 找不到就静默（原版也是直接不做事）
	}
	it := p.bagAt(idx)
	tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
	x, y := s.dropPosition(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), dropRangePlayerThrow, p.Obj.ID)
	s.takeBagItem(p, idx)
	s.spawnGroundItem(p, it, x, y)
	s.send(c, proto.SM_DROPITEM_SUCCESS, m.Head.Recog, 0, 0, 0, m.Body)
	s.sendBagItems(c, p)
	name := ""
	if tmpl != nil {
		name = tmpl.Name
	}
	log.Printf("%s 丢弃 %s 于 (%d,%d)", p.Char.Name, name, x, y)
}

func (s *Server) spawnGroundItem(p *Player, it *pb.UserItem, x, y int) {
	// ⚠️ 修两个真 bug（2026-10-06 写 CM_DROPITEM 的测试时撞出来的）：
	//  1. `Get(int(it.Index))` **少了 -1**：物品表是 0-based、`Index` 是 1-based
	//    （`NewStdItemSet` 强制 `Index == i+1`），全仓其它 9 处都写 `-1`，只有这里是旧的
	//     ⇒ 地面物品的名字/外观取到**下一件**物品（掉落图标张冠李戴）。
	//  2. `name := tmpl.Name` 写在 nil 判断**之前** ⇒ 越界（最后一件物品）必 panic。
	tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
	name := ""
	looks := uint16(0)
	if tmpl != nil {
		name, looks = tmpl.Name, tmpl.Looks
	}
	gi := &GroundItem{
		ID:        s.world.groundSeq.Add(1),
		Name:      name,
		Item:      it,
		Map:       p.Obj.MapRef(),
		X:         x,
		Y:         y,
		Looks:     looks,
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.world.ground[gi.ID] = gi
	s.mu.Unlock()

	s.broadcastGroundItem(gi)
}
