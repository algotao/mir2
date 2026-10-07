package data

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// NPC 是非战斗 NPC（商人 / 功能 NPC）。
type NPC struct {
	// ID 是商人编号，对应 market_def/<ID>-<地图>.txt（脚本与商品）。
	// 普通 NPC 没有脚本，ID 为空。
	ID string
	// Name 是显示名。
	Name string
	// MapID 是所在地图号。
	MapID string
	X     int
	Y     int
	// RaceImg 是外观（merchant.txt 的"主要部分"列）。
	RaceImg int
	// Race 仅 Npcs.txt 有：0=店主 1=国王 2=沙巴克城堡官员。
	Race int
	// Body 是身体外观（Npcs.txt 的 bTile）。
	Body int
	// IsMerchant 区分商人（有商店）与普通 NPC。
	IsMerchant bool
	// Castle 是"属沙城"标记（merchant.txt 最后一列 =1）。原版 `m_boCastle`，
	// 决定在这个 NPC 处交易要不要抽城堡税（见 entity.Monster.CastleNPC）。
	Castle bool
}

// LoadMerchants 解析 merchant.txt。
//
// 格式（TAB 分隔，含大量对齐空格）：
//
//	<ID>  <地图>  <X>  <Y>  <名称>  <方向>  <外观>  <城堡标志>
//
// 例：`1Bme   0102   9   7   屠夫   0   11   0`
//
// ⚠️ 名称可能含空格，但它夹在坐标与数字之间，只能按位置取：
// 先切字段，名称是**第一个非数字字段**之后直到倒数第 3 个字段之前的部分。
func LoadMerchants(path string) ([]*NPC, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []*NPC
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		x, e1 := strconv.Atoi(f[2])
		y, e2 := strconv.Atoi(f[3])
		img, e3 := strconv.Atoi(f[len(f)-2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		// 名称是 f[4] 到 f[len-3]（可能含空格）
		// 用空格连接：名称若含空格要保留原样，
		// 用 "" 拼接会把空格吞掉（"比奇 老兵" → "比奇老兵"）
		name := strings.Join(f[4:len(f)-3], " ")
		if name == "" {
			name = f[4]
		}
		// 最后一列 = "属沙城"（原版 `@NPC ... 属沙城(0,1)` 的 sParam4 → m_boCastle）
		castle := f[len(f)-1] == "1"
		out = append(out, &NPC{
			ID: f[0], MapID: f[1], X: x, Y: y,
			Name: name, RaceImg: img, IsMerchant: true, Castle: castle,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读 merchant: %w", err)
	}
	return out, nil
}

// LoadNpcs 解析 Npcs.txt。
//
// 格式（空白分隔）：`<名称> <race> <地图> <X> <Y> <fTile> <bTile>`
// 例：`沙巴克城堡官员   2   0150   7   16   0   8`
//
// ⚠️ 名称**可能含空格**（如"沙巴克城堡官员"），但这里名称在最前，
// 因此从末尾反推字段位置更可靠。
func LoadNpcs(path string) ([]*NPC, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []*NPC
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		// 末尾 6 个是数字：race 地图 x y fTile bTile
		race, e0 := strconv.Atoi(f[len(f)-6])
		mapID := f[len(f)-5]
		x, e1 := strconv.Atoi(f[len(f)-4])
		y, e2 := strconv.Atoi(f[len(f)-3])
		body, e3 := strconv.Atoi(f[len(f)-1])
		if e0 != nil || e1 != nil || e2 != nil {
			continue
		}
		if e3 != nil {
			body = 0
		}
		name := strings.Join(f[:len(f)-6], " ")
		out = append(out, &NPC{
			Name: name, MapID: mapID, X: x, Y: y, Race: race, Body: body,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读 Npcs: %w", err)
	}
	return out, nil
}

// InMap 返回某地图上的 NPC。
func NPCsInMap(all []*NPC, mapID string) []*NPC {
	var out []*NPC
	for _, n := range all {
		if n.MapID == mapID {
			out = append(out, n)
		}
	}
	return out
}
