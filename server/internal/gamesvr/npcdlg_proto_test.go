package gamesvr

import (
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/world"
	"github.com/algotao/mir2/server/protocol"
)

// TestProtoNpcDialog 是新协议 NPC 对话的**端到端**主链路（用户 2026-10-09：
// 「点击 NPC 无法弹出对话」）。
//
// 为什么必须是端到端而不是"调函数"：那次的根因不在对话引擎，而在**出口** ——
// `showLabel` 只发 legacy，而 proto 玩家的 legacy 下行会被 `protoDown` 丢掉
// ⇒ 引擎跑得好好的、客户端一个字都收不到。所以这条用例从句柄一路走到
// "TCP 上真的收到 NpcSay"，把出口也钉住。
//
// 覆盖：
//
//	① 点 NPC → NpcSay（正文 + 结构化选项，顺序与 index 从 1 起）
//	② 选一个普通标签 → 对白**推进**（第二个 NpcSay 是新标签的正文）
//	③ 选到商店入口（@buy）→ 明说"商店还没接新协议"（不静默、也不再是 legacy 黑洞）
func TestProtoNpcDialog(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	// 玩家在 (1,1)；NPC 挨着他放 (2,1)：距离 1，满足 `onNpcClick` 的 ≤ 8 校验。
	npc := newTestMonster(proto.NpcIDBase+1, "屠夫", 999999)
	npc.IsNPC = true
	npc.Object.SetPlace(s.world.defaultMap, 2, 1, entity.DirDown)
	s.world.monsters[npc.ID] = npc
	s.world.monsterIdx.Add(npc)

	// NPC 定义与脚本：直接注入（脚本走内存缓存，不落盘）。
	s.npc.defs = []*data.NPC{{
		ID: "1Bme", Name: "屠夫", MapID: "0", X: 2, Y: 1, IsMerchant: true, RaceImg: 11,
	}}
	sc, err := script.Parse("1Bme-0", strings.NewReader(`
[@main]
要不要来点肉？\
<我要买/@buy>\
<打听点事/@ask>

[@ask]
最近南边不太平，出村小心。\
<知道了/@exit>
`))
	if err != nil {
		t.Fatalf("解析测试脚本: %v", err)
	}
	s.npc.scripts = map[string]*script.Script{"1Bme-0": sc}

	cl, ev := protoEnterWorld(t, addr, s, sessionID, charID)

	// ① 点 NPC ⇒ NpcSay
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcClick{
		NpcClick: &protocol.NpcClick{NpcId: uint64(npc.ID)}}})
	say := waitNpcSay(t, cl, ev).GetNpcSay()
	if say.GetNpcId() != uint64(npc.ID) {
		t.Errorf("NpcSay.npc_id = %d，应为 %d", say.GetNpcId(), npc.ID)
	}
	if !strings.Contains(say.GetText(), "要不要来点肉") {
		t.Errorf("NpcSay.text = %q，应带上 [@main] 的正文字", say.GetText())
	}
	// 正文要带**行内标记**（`<文字/@序号>`）：客户端靠它把「打开」画在原行、并知道回哪个序号。
	// 用户 2026-10-09「交易窗口渲染不对，应该为『打开 交易市场』在一行」就是指这个。
	if !strings.Contains(say.GetText(), "<我要买/@1>") {
		t.Errorf("NpcSay.text = %q，应含行内标记 `<我要买/@1>`（序号 1 起、与 options 次序一致）", say.GetText())
	}
	opts := say.GetOptions()
	if len(opts) != 2 || opts[0].GetIndex() != 1 || opts[0].GetText() != "我要买" ||
		opts[1].GetIndex() != 2 || opts[1].GetText() != "打听点事" {
		t.Fatalf("选项 = %+v，应为 [(1,我要买) (2,打听点事)]", opts)
	}

	// ② 选普通标签 ⇒ 对白推进到 [@ask]
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcSelect{
		NpcSelect: &protocol.NpcSelect{NpcId: uint64(npc.ID), Index: 2}}})
	say2 := waitNpcSay(t, cl, ev).GetNpcSay()
	if !strings.Contains(say2.GetText(), "最近南边不太平") {
		t.Errorf("推进后的正文 = %q，应为 [@ask] 那段", say2.GetText())
	}
	if n := len(say2.GetOptions()); n != 1 || say2.GetOptions()[0].GetText() != "知道了" {
		t.Errorf("推进后的选项 = %+v", say2.GetOptions())
	}

	// ③ 此刻 [@ask] 只剩一项 = `<知道了/@exit>` ⇒ 对话关掉。
	//
	// ⚠️ 服务端必须**主动下行 `NpcClose`**（用户 2026-10-09 报的 bug：点脚本里的「退出」
	// 窗口不关、按 ESC 才关 —— 那是客户端本地清的）。只清服务端状态、不下行就是那个表现。
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcSelect{
		NpcSelect: &protocol.NpcSelect{NpcId: uint64(npc.ID), Index: 1}}})
	closeEnv := cl.waitFor(ev, "NpcClose", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_NpcClose)
		return ok
	})
	if got := closeEnv.Body.(*protocol.Envelope_NpcClose).NpcClose.GetNpcId(); got != uint64(npc.ID) {
		t.Errorf("NpcClose.npc_id = %d，应为 %d", got, npc.ID)
	}
	drainUntilPong(t, cl, ev, "选 @exit 之后不该再有 NpcSay")

	// ④ 再点一次 NPC ⇒ 重新回到 [@main]（对话状态不是一次性用完就废）
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcClick{
		NpcClick: &protocol.NpcClick{NpcId: uint64(npc.ID)}}})
	say4 := waitNpcSay(t, cl, ev).GetNpcSay()
	if !strings.Contains(say4.GetText(), "要不要来点肉") {
		t.Errorf("重开后应回到 [@main]，实得 %q", say4.GetText())
	}

	// ⑤ 这时第 1 项才是 @buy ⇒ 明说"商店还没接新协议"（不是没事发生，也不是 legacy 黑洞）
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcSelect{
		NpcSelect: &protocol.NpcSelect{NpcId: uint64(npc.ID), Index: 1}}})
	say5 := waitNpcSay(t, cl, ev).GetNpcSay()
	if !strings.Contains(say5.GetText(), "商店") {
		t.Errorf("点商店入口该有一条说明，实得 %q", say5.GetText())
	}

	// ⑥ 距离校验：够不着就不给对话（把 NPC 挪到 9 格外再点）
	//
	// ⚠️ 必须**从世界里重新取**那个 NPC：进图时 `spawnNpcsForMap` 会按 NPC 定义再生成
	// 一个（id 也落在 NpcIDBase 段），前面手工放的那个指针已经不在 `world.monsters` 里了
	// —— 拿旧指针挪是挪不动的，用例会假绿/假红。
	live := s.world.monsters[npc.ID]
	if live == nil {
		t.Fatal("世界里没有那个 NPC")
	}
	live.Object.SetPlace(s.world.defaultMap, 30, 30, entity.DirDown)
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcClick{
		NpcClick: &protocol.NpcClick{NpcId: uint64(npc.ID)}}})
	drainUntilPong(t, cl, ev, "隔得太远不该开对话")
}

// TestProtoNpcClickSendsShopList 点 NPC 时**商品列表必须一起下来**。
//
// ⚠️ 这是 2026-10-10 补的一个真缺口：`onNpcClick` 原来只发 `NpcSay`、不调
// `sendGoods` ⇒ 新协议玩家**永远收不到 `ShopList`** ⇒ 客户端无从知道货架上有什么
// （表现是"点商人没有商店窗"）。legacy 的 `handleClickNPC` 是 `startDialog` +
// `sendGoods` 连着发的（原版也是 `SM_MERCHANTDLG` + `SM_SENDGOODSLIST` 一起来），
// 所以这条把"新协议与 legacy 同构"钉住。
func TestProtoNpcClickSendsShopList(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	// 屠夫（名字带"肉" ⇒ 商品分类能匹配上，见 `shopCategory`）。
	// 玩家在 (1,1)，NPC 放 (2,1)：距离 1，满足 `onNpcClick` 的 ≤ 8 校验。
	npc := newTestMonster(proto.NpcIDBase+1, "屠夫", 999999)
	npc.IsNPC = true
	npc.Object.SetPlace(s.world.defaultMap, 2, 1, entity.DirDown)
	s.world.monsters[npc.ID] = npc
	s.world.monsterIdx.Add(npc)
	s.npc.defs = []*data.NPC{{
		ID: "1Bme", Name: "屠夫", MapID: "0", X: 2, Y: 1, IsMerchant: true, RaceImg: 11,
	}}

	cl, ev := protoEnterWorld(t, addr, s, sessionID, charID)
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_NpcClick{
		NpcClick: &protocol.NpcClick{NpcId: uint64(npc.ID)}}})
	// ⚠️ 不能用 `waitFor`：点 NPC 之后背包/能力值这些"自身状态推送"也会跟着来
	//（`sendBagItems` 是全量口），`waitFor` 会把它们当"无关消息"直接判红。
	// 所以这里自己转一圈，只挑 `ShopList`。
	var got *protocol.ShopList
	for i := 0; i < 200; i++ {
		e := cl.recv()
		if l, ok := e.Body.(*protocol.Envelope_ShopList); ok {
			got = l.ShopList
			break
		}
		if isSelfStatePush(e) {
			continue
		}
		if !ev.note(e) {
			t.Fatalf("等 ShopList 时收到无关消息 %T", e.Body)
		}
	}
	if got == nil {
		t.Fatal("等 ShopList 超时：点 NPC 该把商品列表一起发下来")
	}
	if got.GetNpcId() != uint64(npc.ID) {
		t.Errorf("ShopList.npc_id = %d，应为 %d（客户端靠它回 `ShopBuy`）", got.GetNpcId(), npc.ID)
	}
	// 件数**不**断言：它取决于这张表里有几条该 StdMode 的商品（测试服可能没有）；
	// 这里只钉"会发、且带着正确的商人 id"。
}

// drainUntilPong 发一条 Ping 当"水位线"，并断言在这条 Pong 之前**没有** NpcSay。
//
// 为什么要这么绕：协议里没有"这条请求被拒了"的回包（`onNpcClick` 对够不着/点空了
// 是**静默忽略**），所以"什么都没来"只能用一条已知会回的 Ping 当水位线来判。
func drainUntilPong(t *testing.T, cl *protoClient, ev *protoEvents, why string) {
	t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 1}}})
	for i := 0; i < 200; i++ {
		e := cl.recv()
		if isPong(e) {
			return
		}
		if _, ok := e.Body.(*protocol.Envelope_NpcSay); ok {
			t.Fatalf("%s（收到了 NpcSay）", why)
		}
		ev.note(e)
	}
	t.Fatal("等 Pong 超时")
}

// protoEnterWorld 把一个新协议客户端推到"已进世界"，返回它与实体事件记录器。
//
// 与 `TestProtoContractEnterWorld` 走的是同一串（hello → Reconnect → ListCharacters
// → SelectCharacter → EnterWorld），这里只留最少的分支判断。
func protoEnterWorld(t *testing.T, addr string, s *Server, sessionID int32, charID uint64) (*protoClient, *protoEvents) {
	t.Helper()
	cl := dialProto(t, addr)
	cl.hello()
	ev := &protoEvents{}

	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Reconnect{
		Reconnect: &protocol.Reconnect{SessionToken: sessionTokenV0(sessionID)}}})
	cl.waitFor(ev, "ReconnectResult", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_ReconnectResult)
		return ok
	})
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ListCharacters{
		ListCharacters: &protocol.ListCharacters{}}})
	cl.waitFor(ev, "CharacterList", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_CharacterList)
		return ok
	})
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacter{
		SelectCharacter: &protocol.SelectCharacter{CharacterId: charID}}})
	cl.waitFor(ev, "SelectCharacterResult", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_SelectCharacterResult)
		return ok
	})
	cl.waitFor(ev, "EnterWorld", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_EnterWorld)
		return ok
	})
	// 进图序列的尾巴：`AbilityUpdate` 紧跟在 `EnterWorld` 后面（顺序是契约），
	// 这里一并收掉 —— 否则后面的 `waitFor` 只放过实体事件，会被它判成"顺序错"。
	cl.waitFor(ev, "AbilityUpdate", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_AbilityUpdate)
		return ok
	})
	return cl, ev
}

// waitNpcSay 等到一条 NpcSay。
//
// ⚠️ 不能直接用 `cl.waitFor`：它只放过**实体事件**，而 `AbilityUpdate` 这类
// 单向状态消息随时会插进来（进图后必有一条，升级/回血时还会有）—— 那是完全正常的
// 流量，不是"顺序错"。这里把"实体事件 + 能力值"都记下后跳过，其余才判失败。
func waitNpcSay(t *testing.T, cl *protoClient, ev *protoEvents) *protocol.Envelope {
	t.Helper()
	for i := 0; i < 200; i++ {
		e := cl.recv()
		if _, ok := e.Body.(*protocol.Envelope_NpcSay); ok {
			return e
		}
		// 正常流量（与"顺序错"无关）：能力值随时会推；背包/已穿戴在进图与每次
		// 拾取/穿戴/买卖后都会整份重发（`sendBagItems` 是全量口）。
		//
		// `ShopList` 也是**点 NPC 就该来的**：原版点商人时"对话 + 货架"是一起来的
		//（`SM_MERCHANTDLG` + `SM_SENDGOODSLIST`），legacy 的 `handleClickNPC`
		// 就是 `startDialog` + `sendGoods` 连着发 ⇒ 这里按正常流量跳过。
		switch e.Body.(type) {
		case *protocol.Envelope_AbilityUpdate,
			*protocol.Envelope_BagItems,
			*protocol.Envelope_EquippedItems,
			*protocol.Envelope_ShopList:
			continue
		}
		if !ev.note(e) {
			t.Fatalf("等 NpcSay 时收到无关消息 %T", e.Body)
		}
	}
	t.Fatal("等 NpcSay 超时（收了 200 条还没到）")
	return nil
}

// 让编译器别抱怨 storage 没被用到（protoContractServer 返回的 store 类型来自它）。
var _ = storage.SessionRecord{}

// TestNpcDefOfSameName 同名 NPC 必须**按坐标**认人。
//
// 比奇省有 3 个"屠夫"、2 个"铁匠铺老板"（`merchant.txt`），各自是不同脚本 id
// ⇒ 只按名字匹配会给玩家开别人家的对白/商品。
func TestNpcDefOfSameName(t *testing.T) {
	s := testSlaveServer()
	s.npc.defs = []*data.NPC{
		{ID: "1Bme", Name: "屠夫", MapID: "0", X: 313, Y: 271, IsMerchant: true},
		{ID: "1Gme", Name: "屠夫", MapID: "0", X: 649, Y: 591, IsMerchant: true},
	}
	m := world.Generate("0", 700, 700, false)
	mk := func(id uint32, x, y int) *entity.Monster {
		mo := newTestMonster(id, "屠夫", 1)
		mo.IsNPC = true
		mo.Object.SetPlace(m, x, y, entity.DirDown)
		return mo
	}
	a, b := mk(proto.NpcIDBase+1, 313, 271), mk(proto.NpcIDBase+2, 649, 591)
	if got := s.npcDefOf(a); got == nil || got.ID != "1Bme" {
		t.Errorf("(313,271) 的屠夫该认 1Bme，实得 %+v", got)
	}
	if got := s.npcDefOf(b); got == nil || got.ID != "1Gme" {
		t.Errorf("(649,591) 的屠夫该认 1Gme（同名也不能认错人），实得 %+v", got)
	}
	// 坐标对不上的（配置里没这家）退回名字，别返回 nil
	c := mk(proto.NpcIDBase+3, 100, 100)
	if got := s.npcDefOf(c); got == nil {
		t.Error("坐标对不上时应退回名字匹配，不该返回 nil")
	}
}
