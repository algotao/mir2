// 宠物经验与等级（原版 `TBaseObject.GainSlaveExp`，ObjBase.pas:2268-2299）+ 宠物名色。
//
// 原版逐字（含注释掉的旧公式，说明这里改过算法）：
//
//	procedure TBaseObject.GainSlaveExp(nLevel: Integer);
//	  function GetUpKillCount(): Integer;
//	  begin
//	    if m_btSlaveExpLevel < SLAVEMAXLEVEL - 2 then
//	      tCount := g_Config.MonUpLvNeedKillCount[m_btSlaveExpLevel]
//	    else tCount := 0;
//	//    Result := ((m_Abil.Level shl 4) - m_Abil.Level) + 100 + tCount      ← 旧式
//	    Result := ((m_Abil.Level * g_Config.nMonUpLvRate{16}) - m_Abil.Level)
//	              + g_Config.nMonUpLvNeedKillBase{100} + tCount;
//	  end;
//	begin
//	  Inc(n294, nLevel);
//	  if GetUpKillCount() < n294 then begin
//	    Dec(n294, GetUpKillCount);
//	    if m_btSlaveExpLevel < (m_btSlaveMakeLevel * 2 + 1) then begin
//	      Inc(m_btSlaveExpLevel);
//	      RecalcAbilitys();
//	      RefNameColor();          // ← 宠物名色跟着等级变
//	    end;
//	  end;
//	end;
//
// 触发点（ObjBase.pas:20845-20850）：经验分配时**只有"击杀者本身是宠物"**才涨
// 宠物经验（主人自己打怪不给宠物涨级）：
//
//	if m_ExpHitter.m_Master <> nil then begin
//	  m_ExpHitter.GainSlaveExp(m_Abil.Level);            // ← 这里，按**死者等级**加
//	  tExp := m_ExpHitter.m_Master.CalcGetExp(...);      // 主人照常分经验
//
// 配置值全部来自官方 `!setup.txt`（与 M2Share.pas:1641-1643 的出厂默认一致）：
//
//	MonUpLvNeedKillBase=100   MonUpLvRate=16
//	MonUpLvNeedKillCount0..7 = 0,0,50,100,200,300,600,1200
//	SlaveColor0..8          = 255,254,147,154,...（见下）
//
// ⚠️ 现状：机制的**触发点还没有** —— 我们的宠物 AI（`tickSlaves`）目前只打
// **玩家**（`slaveShouldAttack` 的循环是 `for _, v := range s.world.players`），而原版
// 宠物打的是"主人的目标"（可以是怪）。所以 `gainSlaveExp` 目前只被单测调用，
// 等宠物 AI 补上打怪后接进去（见 progress §2.4）。
package gamesvr

import (
	"log"

	"github.com/algotao/mir2/server/internal/entity"
)

const (
	// slaVeMaxLevel 原版 SLAVEMAXLEVEL（Grobal2.pas:1087）。
	slaveMaxLevel = 50
	// slaveMonUpLvRate / slaveMonUpLvNeedKillBase 官方 !setup.txt:108-109。
	slaveMonUpLvRate         = 16
	slaveMonUpLvNeedKillBase = 100
)

// slaveUpKillCount 是升到下一级需要的击杀点数（原版 GetUpKillCount）。
//
//	((宠物等级 * 16) - 宠物等级) + 100 + MonUpLvNeedKillCount[宝宝等级]
//	（宝宝等级 ≥ SLAVEMAXLEVEL-2 时最后一项取 0）
func slaveUpKillCount(petLevel, expLevel int) int {
	tCount := 0
	if expLevel < slaveMaxLevel-2 {
		tCount = slaveMonUpLvNeedKillCount(expLevel)
	}
	return (petLevel*slaveMonUpLvRate - petLevel) + slaveMonUpLvNeedKillBase + tCount
}

// slaveMonUpLvNeedKillCount 是官方 !setup.txt:110-117 的 8 项表。
func slaveMonUpLvNeedKillCount(expLevel int) int {
	table := [8]int{0, 0, 50, 100, 200, 300, 600, 1200}
	if expLevel < 0 || expLevel >= len(table) {
		return 0
	}
	return table[expLevel]
}

// slaveColor 是宠物名色表（官方 !setup.txt:118+，M2Share.pas:1643 同值）。
//
// 索引 = 宝宝等级（0 起）；原版 `GetNamecolor` 里
// `if m_btSlaveExpLevel < SLAVEMAXLEVEL then Result := SlaveColor[m_btSlaveExpLevel]`
// （ObjBase.pas:19077-19078），超出则用客户端默认色。
var slaveColor = [9]uint8{0xFF, 0xFE, 0x93, 0x9A, 0xE5, 0xA8, 0xB4, 0xFC, 249}

// slaveColorOf 返回宠物当前名色（0 = 用默认色）。
func slaveColorOf(m *entity.Monster) uint8 {
	if m == nil || int(m.SlaveExpLevel) >= len(slaveColor) {
		return 0
	}
	return slaveColor[m.SlaveExpLevel]
}

// gainSlaveExp 给宠物加击杀点数（原版 GainSlaveExp）。返回是否升级了。
//
// 参数 nLevel = **被杀者的等级**（原版 `GainSlaveExp(m_Abil.Level)`，死者自己的等级）。
func (s *Server) gainSlaveExp(m *entity.Monster, nLevel int) bool {
	if m == nil || nLevel <= 0 {
		return false
	}
	petLevel := 0
	if m.Info != nil {
		petLevel = int(m.Info.Level)
	}
	need := slaveUpKillCount(petLevel, int(m.SlaveExpLevel))
	if need <= 0 {
		return false
	}
	m.AddSlaveKills(nLevel)
	if m.SlaveKills() <= need {
		return false
	}
	m.AddSlaveKills(-need)
	up := false
	if int(m.SlaveExpLevel) < int(m.SlaveMakeLevel)*2+1 {
		m.SlaveExpLevel++
		up = true
		// 原版在这里调 RecalcAbilitys()：对怪物而言它只重算 AC/MAC 缓存，
		// 我们的怪物属性直接取模板（见 entity.Monster 的说明）⇒ 没有对应动作。
		// 但**名色必须刷**（原版下一行就是 RefNameColor）。
		s.refShowName(m)
		log.Printf("%s 的宠物等级升到 %d（召唤等级 %d，上限 %d）",
			s.masterNameOf(m), m.SlaveExpLevel, m.SlaveMakeLevel, int(m.SlaveMakeLevel)*2+1)
	}
	return up
}

// masterNameOf 给日志用：宠物主人的名字（没有则"无主"）。
func (s *Server) masterNameOf(m *entity.Monster) string {
	if m == nil {
		return "无主"
	}
	if p := s.playerByActorID(m.MasterID); p != nil && p.Char != nil {
		return p.Char.Name
	}
	return "无主"
}
