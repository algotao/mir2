// Package chargen 建角色的**唯一一份**实现。
// !
// ! # 为什么要有这个包
// !
// ! 建角色的入口有两条：legacy 的 `accountsvc`（`CM_NEWCHR`）与新协议的 `gamesvr`
// ! （`CreateCharacter`）。它们必须产生**一模一样的角色**（同一个名字规则、
// ! 同一套初始物品、同样 13 槽的装备位）—— 两边各写一遍必然漂移，
// ! 而症状是"用旧客户端建的角色和新客户端建的角色不一样"（R-7）。
// !
// ! 这里只放**与传输无关**的东西：校验、初始值、组装。存储写入由调用方做
// ! （两边用的是同一个 `storage.Characters()`）。
package chargen

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

// MaxPerAccount 每账号角色上限（原版为 2，`UsrSoc.pas:652`）。
const MaxPerAccount = 2

// EquipSlots 装备位定长：原版 `THumItems = array[0..12]`。
//
// ⚠️ 空槽也要留 `Index=0` 的占位 —— `repeated` 字段必须保留槽位信息，
// 否则"穿在哪一格"会错乱。
const EquipSlots = 13

// MinNameBytes 角色名最短**字节数**。
//
// ⚠️ 按字节而不是按字符：legacy 就是这么写的（`validChrName` 用 `len`）。
// 对汉字来说这意味着"至少 1 个字"（1 个汉字 3 字节），
// 与"至少 3 个字符"**不是**一回事 —— 这里保持原样，免得建角行为悄悄变了。
const MinNameBytes = 3

// badNameChars 角色名里不允许出现的字符（与 legacy 一致）。
//
// 第一个是空格：名字里有空格会把 legacy 那套 `/` 分隔的正文切错段。
const badNameChars = " \t/@?'"

// ValidName 角色名规则（原版 + legacy 的实际做法）。
func ValidName(name string) bool {
	if len(name) < MinNameBytes {
		return false
	}
	return !strings.ContainsAny(name, badNameChars)
}

// ItemSource 建角时按名字取物品模板。
//
// `data.StdItemSet` 天然满足它（`GetByName(name) *StdItem`）——
// 用接口是为了让单测不必加载整张物品表。
type ItemSource interface {
	GetByName(name string) *data.StdItem
}

// InitialHPMP 新角色的初始血/蓝（按职业）。
//
// ⚠️ 直接委派给 `entity.InitialHPMP`：**等级上限的公式同源**，
// 否则"建角时写进档的上限"和"进游戏后重算出来的上限"会对不上
// （见 `entity.InitialHPMP` 的注释）。
func InitialHPMP(job uint32) (hp, mp uint32) {
	return entity.InitialHPMP(job)
}

// InitialItems 生成新角色的初始物品。
//
// **原版 1.76 出生自带三件：布衣、木剑、蜡烛**（口径见 `docs/use.md`：
// 「两个村子都属于比奇省，是安全区，出生自带：布衣、木剑、蜡烛，1 级」）。
// 本仓早先给的是"木剑 + 5 瓶金创药"—— 与这份口径不符，已改成原版那三件
// （要药水就再往 `bag` 里加，见 D-40）。
//
// 槽位出处：`Grobal2.pas:29-30` 的 `U_DRESS=0` / `U_WEAPON=1`；
// 蜡烛按 `M2Share.pas:3519`（`U_RIGHTHAND`：`StdMode` 28/29/30 拿在**右手**）
// ⇒ 我们物品表里 `蜡烛` 的 `std_mode` 正好是 30 ⇒ 槽 2。
//
// `seq` 是**物品实例号**（`MakeIndex`）分配器：两个服务各有自己的一个。
func InitialItems(items ItemSource, seq *atomic.Int64, sex uint32) (equip map[uint32]*pb.UserItem, bag []*pb.UserItem) {
	if items == nil {
		return nil, nil
	}
	newItem := func(name string) *pb.UserItem {
		it := items.GetByName(name)
		if it == nil {
			return nil
		}
		return &pb.UserItem{
			MakeIndex: int32(seq.Add(1)),
			Index:     uint32(it.Index), // data.StdItem.Index 是 int32，pb 用 uint32
			Dura:      it.DuraMax,
			DuraMax:   it.DuraMax,
		}
	}

	// 衣服分男女：原版是**两件不同的物品**（`布衣(男)` StdMode 10 / `布衣(女)` 11）
	cloth := "布衣(男)"
	if sex != 0 {
		cloth = "布衣(女)"
	}
	equip = map[uint32]*pb.UserItem{}
	if u := newItem(cloth); u != nil {
		equip[0] = u // U_DRESS
	}
	if u := newItem("木剑"); u != nil {
		equip[1] = u // U_WEAPON
	}
	if u := newItem("蜡烛"); u != nil {
		equip[2] = u // U_RIGHTHAND（StdMode 30 = 蜡烛/火把，见上）
	}
	return equip, bag
}

// PickHome 从候选出生点里**随机**挑一个。
//
// **原版 1.76：新角色在两个新手村之间随机，不分职业**（`docs/use.md`）——
// 银杏山谷（我们安全点表里的 `0 650 631`）与 边界村（`0 289 618`）；
// 用户 2026-10-08 的口径也是"原版是在 649 627 的地方出生，银杏谷"（±几格 = 安全区内）。
//
// 空列表 ⇒ 零值（调用方退化成"只给地图号"）；**单个候选 ⇒ 等价于钉死**
// （调试与单测要确定性时就用它）。
func PickHome(points []Home) Home {
	if len(points) == 0 {
		return Home{}
	}
	return points[rand.IntN(len(points))]
}

// ParseHomePoints 解析命令行那种 `x,y;x,y` 出生点候选（地图号统一用 `mapID`）。
//
// 放在 chargen 里是为了**两条建角路共用同一份解析与默认值**（gamesvr 的 `-home-points`、
// accountsvc 的 `-home-points`）—— 与 `Build` 同一条纪律：两边各写一遍必然漂移（R-7）。
//
// 容忍空白与多余的分号；**空结果算错误**（宁可启动就炸，也不要静默变成"没有候选"）。
func ParseHomePoints(spec, mapID string) ([]Home, error) {
	var out []Home
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		xy := strings.Split(part, ",")
		if len(xy) != 2 {
			return nil, fmt.Errorf("出生点 %q 不是 `x,y` 形式", part)
		}
		x, errX := strconv.Atoi(strings.TrimSpace(xy[0]))
		y, errY := strconv.Atoi(strings.TrimSpace(xy[1]))
		if errX != nil || errY != nil || x < 0 || y < 0 {
			return nil, fmt.Errorf("出生点 %q 不是合法坐标", part)
		}
		out = append(out, Home{Map: mapID, X: uint32(x), Y: uint32(y)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("出生点候选是空的（%q）", spec)
	}
	return out, nil
}

// DefaultHomePoints 是**两个新手村**（原版 1.76 的出生候选，见 `docs/use.md`）。
//
// 银杏山谷 `0 650 631` 与 边界村 `0 289 618` —— 这两条也确实都在
// `server/data/envir/StartPoint.txt` 里（安全区/复活点表）。
func DefaultHomePoints(mapID string) []Home {
	return []Home{
		{Map: mapID, X: 650, Y: 631}, // 银杏山谷
		{Map: mapID, X: 289, Y: 618}, // 边界村
	}
}

// Home 新角色的出生点。
type Home struct {
	Map  string
	X, Y uint32
	Dir  uint32
}

// Build 组装一个新角色（**不写库**，调用方负责 `Create`）。
//
// `job` 是 0/1/2（战/法/道），`sex` 是 0/1 —— 与存档、与 legacy 正文的约定一致。
// `home` 是出生点：原版建角就写进"当前坐标"与"回城点"，两者相同。
func Build(
	account, name string,
	job, sex, hair uint32,
	home Home,
	items ItemSource,
	seq *atomic.Int64,
) *storage.Character {
	hp0, mp0 := InitialHPMP(job)
	equipInit, bagInit := InitialItems(items, seq, sex)

	humItems := make([]*pb.UserItem, EquipSlots)
	for i := range humItems {
		humItems[i] = &pb.UserItem{} // 空槽占位：见 EquipSlots 的说明
	}
	for slot, u := range equipInit {
		if int(slot) < EquipSlots && u != nil {
			humItems[slot] = u
		}
	}

	c := &storage.Character{
		Account: account,
		Name:    name,
		Job:     job,
		Level:   1,
		Data: &pb.CharacterData{
			ChrName:  name,
			Account:  account,
			Hair:     hair,
			Job:      job,
			Sex:      sex,
			CurMap:   home.Map,
			CurX:     home.X,
			CurY:     home.Y,
			Dir:      home.Dir,
			HomeMap:  home.Map,
			HomeX:    home.X,
			HomeY:    home.Y,
			Abil:     &pb.Ability{Level: 1, Hp: hp0, Mp: mp0, MaxHp: hp0, MaxMp: mp0},
			HumItems: humItems,
			BagItems: bagInit,
		},
	}
	// 把 Data 里的字段同步到结构体的检索列（`Name`/`Level` 等）——
	// 少了这一步，`ListByAccount` 出来的摘要会是空的（legacy 也调它）。
	c.SyncFromData()
	return c
}
