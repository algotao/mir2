package data

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Envir 配置解析（来自 1.76 官方服务端 Mir2-GeeM2/Envir）。

// MapInfo 是 mapinfo.txt 里的一张地图。
type MapInfo struct {
	// ID 是地图号，对应 Map/<ID>.map 文件名（如 "0"、"0101"）。
	ID string
	// Name 是显示名（如"比奇省"、"沃玛寺庙"）。
	Name string
	// Safe 是安全区：区内禁止攻击玩家（ObjBase.pas:21533 读 MapFlag.boSAFE，
	// 由 LocalDB.pas:574 解析属性行里的 SAFE 关键字写入）。
	Safe bool
	// FightZone 是 PK 区（FIGHT）。
	FightZone bool
	// Fight3Zone 是工会战区（FIGHT3）：区内死亡按行会积分结算
	// （ObjBase.pas:21018-21038）。
	Fight3Zone bool
	// Quiz 是问答/活动区（QUIZ）：**不允许喊话**
	// （ObjBase.pas:8699）。官方 mapinfo 里 7 张图带它。
	Quiz bool

	// ---- 以下按官方 `LocalDB.pas:560-760` 的关键字表逐项解析 ----
	//
	// 「消费」= 服务端真的拿它做了什么；「仅解析」= 已知其存在、值也读进来了，
	// 但暂时没有消费方（写在这里是为了**避免把带参数的形式误判**，也为了下一步补消费）。

	// Darkness / DayLight 是昼夜（DARK / DAY）：**仅解析**（客户端表现）。
	Darkness bool
	DayLight bool
	// NoRecall 禁止**召集类**传送（NORECALL / NOGUILDRECALL / NODEARRECALL /
	// NOMASTERRECALL）：队召、行会召集、夫妻召集、师徒召集各自一套。
	// 消费点：`cmdGroupRecall`（官方 `ObjBase.pas:12950/:12969`）。
	NoRecall       bool
	NoGuildRecall  bool
	NoDearRecall   bool
	NoMasterRecall bool
	// NoRandomMove 禁止在该图随机移动（NORANDOMMOVE）：随机传送卷不能生效
	// （官方 `ObjBase.pas:23539` 的形状 2 分支外层就是它）。**已消费**。
	NoRandomMove bool
	// NoDrug 该图禁用一切药品/消耗品（NODRUG）：官方 `EatItems` 的**第一行**
	// 就是 `if m_PEnvir.Flag.boNODRUG then SysMsg(sCanotUseDrugOnThisMap)…Exit`
	// （`ObjBase.pas:23330`）。**已消费**。
	NoDrug bool
	// Mine / Mine2 是矿区（MINE / MINE2）：挖矿用。**仅解析**
	//（挖矿本身还没实现，见 docs/gap-audit.md §2.2）。
	Mine  bool
	Mine2 bool
	// NoDropItem 该图死亡不掉落（NODROPITEM）：官方 `:20566`/`:20995`。
	// **已消费**（`deathDrop`）。
	NoDropItem bool
	// NoThrowItem 该图禁止把物品丢到地上（NOTHROWITEM）：官方 `:16202`/`:16233`。
	// **已消费**（`CM_DROPITEM`）。
	NoThrowItem bool
	// NoPositionMove 该图禁止定位传送（NOPOSITIONMOVE）：传送戒指 `@move` 不能生效
	//（官方 `ObjBase.pas:15205`）。**已消费**。
	NoPositionMove bool
	// NoChat 该图禁止喊话（NOCHAT），括号里是"允许喊话的等级下限"（官方把它读进 `nL`）。
	// **已消费**（聊天分派）。
	NoChat    bool
	ChatLevel int
	// RunHuman / RunMon：该图是否允许人/怪奔跑（RUNHUMAN/RUNMON）。
	// ⚠️ 官方把它塞进**客户端配置**发下去（`:16825-16833` `ClientConf.boRUNHUMAN`），
	// 我们沿用同一思路但**暂未消费**（服务端没有跑步门禁，跑不跑由客户端决定）。
	RunHuman bool
	RunMon   bool
	// NeedHole 仅解析（NEEDHOLE）。
	NeedHole bool
	// ExpRate 是**地图经验倍率（百分比）** EXPRATE(n)：官方在杀怪经验链上
	// `dwExp := Round((nEXPRATE / 100) * dwExp)`（`ObjBase.pas:1834`）。**已消费**。
	// 0 表示"该图没有设置"。
	ExpRate int
	// PK 四个开关的**地图级覆盖**（PKWINLEVEL / PKWINEXP / PKLOSTLEVEL / PKLOSTEXP）：
	// 官方在 `PKDie` 里把它们盖到全局配置上（`ObjBase.pas:21085-21110`）。
	// `Set=false` 表示该图没设置。**已消费**（pkdie.go）。
	PKWinLevelSet  bool
	PKWinLevel     int
	PKWinExpSet    bool
	PKWinExp       int
	PKLostLevelSet bool
	PKLostLevel    int
	PKLostExpSet   bool
	PKLostExp      int
	// Music / MusicID 仅解析（MUSIC(n)）。
	Music   bool
	MusicID int
	// NoReconnect 禁止重连（NORECONNECT(地图号)）：**仅解析**（我们还没有重连流程）。
	NoReconnect  bool
	ReconnectMap string
	// DecHP / IncHP：该图定时扣血 / 加血（DECHP(点/秒) / INCHP(点/秒)）。**仅解析**。
	DecHPSet   bool
	DecHPPoint int
	DecHPTime  int
	IncHPSet   bool
	IncHPPoint int
	IncHPTime  int
	// NeedSetOn / NeedSetOff 仅解析（NEEDSET_ON(n) / NEEDSET_OFF(n)，GeeM2 的地图开关）。
	NeedSetOn  bool
	NeedSetOff bool
	NeedSetID  int
}

// LoadMiniMap 读官方 `MiniMap.txt`："地图号 小地图编号"（`D001  1`）。
//
// 原版 `TPlayObject.ClientGetMinMap`（ObjBase.pas:17985-17998）就是查这张表：
// 查到编号 > 0 回 `SM_READMINIMAP_OK`（编号放在 Param），否则回 `SM_READMINIMAP_FAIL`。
func LoadMiniMap(path string) (map[string]uint16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]uint16)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n <= 0 {
			continue
		}
		out[fields[0]] = uint16(n)
	}
	return out, sc.Err()
}

// LoadMapInfo 解析 mapinfo.txt。
//
// 格式：`[<地图号> <地图名> <标志>]` + 同行的属性串，属性串是空格分隔的关键字，
// 部分关键字带括号参数（`EXPRATE(200)`、`NORECONNECT(0)`、`NOCHAT(2)`…）。
// 例：`[0 比奇省 0]`、`[G001 质询屋 0] SAFE DAY QUIZ`、`[0162  监狱 0] SAFE`。
//
// 段头第三字段是 LAYOUT（0=常规），原版不参与安全区判定；
// 这些属性来自**属性串**的关键字（官方解析点 `LocalDB.pas:560-760`，
// 逐 token `CompareText` + 赋值到 `TMapFlag`）。
//
// ⚠️ 2026-10-06 之前这里**只认 4 个关键字**（SAFE/FIGHT/FIGHT3/QUIZ），其余全部丢弃。
// 现在按官方那张表把关键字**全部识别**（有的只是解析后暂不消费，见各字段注释），
// 至少保证带参数的形式不会被误当成关键字。
func LoadMapInfo(path string) ([]*MapInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []*MapInfo
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// cur 是正在读属性行的地图段；nil 表示当前行不是段头。
	var cur *MapInfo
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				cur = nil
				continue
			}
			fields := strings.Fields(strings.TrimSpace(line[1:end]))
			if len(fields) < 2 {
				cur = nil
				continue
			}
			cur = &MapInfo{ID: fields[0], Name: fields[1]}
			out = append(out, cur)
			// ⚠️ 官方写法是 `[段头] 属性`——**属性与段头在同一行**，
			// 例如 `[G001 质询屋 0] SAFE DAY QUIZ`。所以 `]` 后面的部分
			// 也要当属性行处理，不能直接跳过（否则一张 SAFE 都读不到）。
			applyMapFlags(cur, line[end+1:])
			continue
		}
		// 独立成行的属性行：归属到上一个段头。
		if cur == nil {
			continue
		}
		applyMapFlags(cur, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读 mapinfo: %w", err)
	}
	return out, nil
}

// StartPoint 是出生 / 复活点。
type StartPoint struct {
	// MapID 对应 Map/<MapID>.map。
	MapID string
	X     int
	Y     int
}

// LoadStartPoints 解析 StartPoint.txt。
//
// 格式：`地图号 <TAB> x <TAB> y <TAB> ...`（后续字段含义未用）。
// 例：`0    289  618	0	10	0	0	0`
func LoadStartPoints(path string) ([]*StartPoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []*StartPoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line) // 兼容空格与 TAB
		if len(fields) < 3 {
			continue
		}
		x, err1 := strconv.Atoi(fields[1])
		y, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, &StartPoint{MapID: fields[0], X: x, Y: y})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读 StartPoint: %w", err)
	}
	return out, nil
}

// applyMapFlags 把一段空格分隔的关键字应用到地图属性上。
//
// 只认 SAFE / FIGHT / FIGHT3 / QUIZ 四个（分别对应 MapFlag.boSAFE /
// boFIGHTZone / boFIGHT3Zone / boQUIZ，ObjBase.pas:21533、21367、21018、8699）；
// 其余关键字（DAY/DARK/NOITEM/FLY/…）本项目尚未使用，直接忽略。
// 注意 `NOCHAT`（禁聊）官方 mapinfo 里 **0 处**使用，故不解析。
func applyMapFlags(mi *MapInfo, text string) {
	for _, tok := range strings.Fields(text) {
		// 拆出关键字与括号参数：`EXPRATE(200)` ⇒ key=EXPRATE、arg="200"。
		// 官方用 `ArrestStringEx(s34, '(', ')', s38)` 取参数，形式就是 `KEY(值)`。
		key, arg := tok, ""
		if i := strings.IndexByte(tok, '('); i >= 0 {
			key = tok[:i]
			if j := strings.LastIndexByte(tok, ')'); j > i {
				arg = tok[i+1 : j]
			}
		}
		switch strings.ToUpper(key) {
		// ---- 已消费 ----
		case "SAFE":
			mi.Safe = true
		case "FIGHT":
			mi.FightZone = true
		case "FIGHT3":
			mi.Fight3Zone = true
		case "QUIZ":
			mi.Quiz = true
		case "NORECALL":
			mi.NoRecall = true
		case "NOGUILDRECALL":
			mi.NoGuildRecall = true
		case "NODEARRECALL":
			mi.NoDearRecall = true
		case "NOMASTERRECALL":
			mi.NoMasterRecall = true
		case "NORANDOMMOVE":
			mi.NoRandomMove = true
		case "NODRUG":
			mi.NoDrug = true
		case "NODROPITEM":
			mi.NoDropItem = true
		case "NOTHROWITEM":
			mi.NoThrowItem = true
		case "NOPOSITIONMOVE":
			mi.NoPositionMove = true
		case "NOCHAT":
			mi.NoChat = true
			mi.ChatLevel = atoiOr(arg, 0)
		case "EXPRATE":
			mi.ExpRate = atoiOr(arg, 0)
		case "PKWINLEVEL":
			mi.PKWinLevelSet, mi.PKWinLevel = true, atoiOr(arg, 0)
		case "PKWINEXP":
			mi.PKWinExpSet, mi.PKWinExp = true, atoiOr(arg, 0)
		case "PKLOSTLEVEL":
			mi.PKLostLevelSet, mi.PKLostLevel = true, atoiOr(arg, 0)
		case "PKLOSTEXP":
			mi.PKLostExpSet, mi.PKLostExp = true, atoiOr(arg, 0)

		// ---- 仅解析（值读进来了，暂时没有消费方）----
		case "DARK":
			mi.Darkness = true
		case "DAY":
			mi.DayLight = true
		case "MINE":
			mi.Mine = true
		case "MINE2":
			mi.Mine2 = true
		case "RUNHUMAN":
			mi.RunHuman = true
		case "RUNMON":
			mi.RunMon = true
		case "NEEDHOLE":
			mi.NeedHole = true
		case "MUSIC":
			mi.Music = true
			mi.MusicID = atoiOr(arg, -1)
		case "NORECONNECT":
			mi.NoReconnect = true
			mi.ReconnectMap = arg
		case "DECHP":
			// 官方：点数与时间两段（形如 `DECHP(10/5)`），点在 `GetValidStr3` 里按分隔符切开。
			mi.DecHPSet = true
			p1, p2 := splitPair(arg)
			mi.DecHPPoint, mi.DecHPTime = atoiOr(p1, 0), atoiOr(p2, -1)
		case "INCHP":
			mi.IncHPSet = true
			p1, p2 := splitPair(arg)
			mi.IncHPPoint, mi.IncHPTime = atoiOr(p1, 0), atoiOr(p2, -1)
		case "NEEDSET_ON":
			mi.NeedSetOn = true
			mi.NeedSetID = atoiOr(arg, -1)
		case "NEEDSET_OFF":
			mi.NeedSetOff = true
			mi.NeedSetID = atoiOr(arg, -1)

		// ---- 识别但**不消费**：GeeM2 的元宝/积分地图消耗（`DECGAMEGOLD(时间)` 等）。
		// 我们不做元宝/积分体系 ⇒ 只保证不被当成未知 token（也就不需要字段）。
		case "DECGAMEGOLD", "DECGAMEPOINT", "INCGAMEGOLD", "INCGAMEPOINT":
			// 有意为空。

		// `NOHORSE`（禁止骑马）官方反编译里被错标成写 `boNOPOSITIONMOVE`
		//（`ObjBase.pas:560-760` 那张表），语义上不是一回事 ⇒ 识别但不消费。
		case "NOHORSE":
		}
	}
}

// atoiOr 解析整数，失败返回 def。
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// splitPair 把 `a/b` 拆成两段（官方 `DECHP`/`INCHP` 用 `GetValidStr3` 按分隔符切）。
func splitPair(s string) (string, string) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
