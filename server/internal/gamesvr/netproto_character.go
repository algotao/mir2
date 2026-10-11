package gamesvr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/authn"
	"github.com/algotao/mir2/server/internal/chargen"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/protocol"
)

// ⚠️ 这几个码是**我们定的**（协议里 `ActionResult.code` 只写了"非 0 见各消息的注释"），
// 所以这里就是它的注释，`protocol/account.proto` 里同步写着同一份。
const (
	createBadClass  = 1 // 职业不是 1/2/3
	createBadName   = 2 // 名字不合规（太短/含禁用字符）
	createNameTaken = 3 // 重名
	createTooMany   = 4 // 该账号角色已满
	createInternal  = 5 // 存储层出错
	deleteBadProof  = 1 // 二次确认（口令证明）没通过
	deleteNotFound  = 2 // 这个角色不属于本账号 / 已经删了
	deleteInternal  = 3 // 存储层出错
)

// onCreateCharacter 建角色（新协议）。
//
// ⚠️ 规矩**一律走 `chargen`** —— 与 legacy 的 `CM_NEWCHR` 是**同一份实现**（R-7）：
// 名字规则、初始物品、13 槽装备位、初始 HP/MP 都在那边。
// 这里只管几道门（职业/重名/数量）与回执，免得两边各长出一套。
func (ps *protoSession) onCreateCharacter(m *protocol.CreateCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	account := ps.rec.Account

	// 职业：新协议 1/2/3 → 存档 0/1/2（与 `characterSummary` 反着来）
	job := int(m.GetClass()) - 1
	if job < 0 || job > 2 {
		return ps.sendCreateResult(false, createBadClass, "职业不对", 0) == nil
	}
	sex := uint32(0)
	if m.GetGender() == protocol.Gender_GENDER_FEMALE {
		sex = 1
	}
	name := strings.TrimSpace(m.GetName())
	if !chargen.ValidName(name) {
		return ps.sendCreateResult(
			false, createBadName, chargen.BadNameMessage(), 0,
		) == nil
	}
	// 重名（**全局**唯一：原版也是全局查重）
	if _, err := ps.srv.store.Characters().GetByName(ctx, name); err == nil {
		return ps.sendCreateResult(false, createNameTaken, "这个名字已经有人用了", 0) == nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return ps.sendCreateResult(false, createInternal, "查重失败", 0) == nil
	}
	// 数量（软删的不算）
	chars, err := ps.srv.store.Characters().ListByAccount(ctx, account)
	if err != nil {
		return ps.sendCreateResult(false, createInternal, "读取角色列表失败", 0) == nil
	}
	alive := 0
	for _, c := range chars {
		if !c.Deleted {
			alive++
		}
	}
	if alive >= chargen.MaxPerAccount {
		return ps.sendCreateResult(
			false, createTooMany,
			fmt.Sprintf("每个账号最多 %d 个角色", chargen.MaxPerAccount), 0,
		) == nil
	}

	var items chargen.ItemSource
	if ps.srv.data.tables != nil {
		items = ps.srv.data.tables.Items
	}
	c := chargen.Build(
		account, name, uint32(job), sex, m.GetHair(), ps.srv.newCharHome(), items, &ps.srv.itemSeq,
	)
	if err := ps.srv.store.Characters().Create(ctx, c); err != nil {
		log.Printf("%s: 建角色 %q 失败: %v", ps.clientIP, name, err)
		return ps.sendCreateResult(false, createInternal, "存档写不进去", 0) == nil
	}
	log.Printf("%s: 建角色 %q（账号 %s 职业 %d 性别 %d）→ id=%d",
		ps.clientIP, name, account, job, sex, c.ID)
	return ps.sendCreateResult(true, 0, "", uint64(c.ID)) == nil
}

// onDeleteCharacter 删角色（**软删**：数据留着，只是不再出现在列表里、也不能进）。
//
// `password_hash` 是**二次确认**，复用登录时那条口令证明：
// 它绑在**本条连接的 nonce** 上（D-24①），所以换个连接就失效、重放不了；
// 服务端拿存储里的 `K` 再算一遍（`authn.CheckProof`，常量时间比较）。
// 原版删角色只弹一个确认框、不带口令 —— 这一道门是我们加的（存储层虽是软删、
// 能救回来，但"删了不可恢复"值得多问一次）。
func (ps *protoSession) onDeleteCharacter(m *protocol.DeleteCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()

	acc, err := ps.srv.store.Accounts().GetByName(ctx, ps.rec.Account)
	if err != nil {
		log.Printf("%s: 删角取账号失败: %v", ps.clientIP, err)
		return ps.sendDeleteResult(false, deleteInternal, "读不到账号") == nil
	}
	proof, err := hex.DecodeString(strings.TrimSpace(m.GetPasswordHash()))
	if err != nil || len(proof) != sha256.Size {
		return ps.sendDeleteResult(false, deleteBadProof, "二次确认的格式不对") == nil
	}
	if !authn.CheckProof(acc.PasswordHash, ps.nonce, ps.rec.Account, proof) {
		return ps.sendDeleteResult(false, deleteBadProof, "口令确认没通过") == nil
	}
	chr, err := ps.findCharacter(ctx, m.GetCharacterId())
	if err != nil {
		return ps.sendDeleteResult(false, deleteNotFound, err.Error()) == nil
	}
	if err := ps.srv.store.Characters().MarkDeleted(ctx, chr.ID); err != nil {
		log.Printf("%s: 删角色 %q 失败: %v", ps.clientIP, chr.Name, err)
		return ps.sendDeleteResult(false, deleteInternal, "删不掉") == nil
	}
	log.Printf("%s: 删角色 %q（账号 %s id=%d）", ps.clientIP, chr.Name, ps.rec.Account, chr.ID)
	return ps.sendDeleteResult(true, 0, "") == nil
}

// sendCreateResult / sendDeleteResult 两条回执（都带 `ActionResult`）。
func (ps *protoSession) sendCreateResult(ok bool, code uint32, msg string, id uint64) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_CreateCharacterResult{
		CreateCharacterResult: &protocol.CreateCharacterResult{
			Result:      &protocol.ActionResult{Ok: ok, Code: code, Message: msg},
			CharacterId: id,
		}}})
}

func (ps *protoSession) sendDeleteResult(ok bool, code uint32, msg string) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_DeleteCharacterResult{
		DeleteCharacterResult: &protocol.DeleteCharacterResult{
			Result: &protocol.ActionResult{Ok: ok, Code: code, Message: msg},
		}}})
}

// newCharHome 新角色的出生点：**默认地图的 StartPoint**（表里没有就 (0,0)）。
//
// ⚠️ 与 legacy 的 `HomeMap/HomeX/HomeY` 是同一语义（那边是配置里的 289,618）——
// 两边的"新号出生在哪"必须是同一个地方。
func (s *Server) newCharHome() chargen.Home {
	// **原版 1.76：两个新手村随机二选一**（银杏山谷 / 边界村，不分职业）——
	// 口径见 `docs/use.md`，候选表是 `-home-points`（默认就是那两个村）。
	//
	// ⚠️ 走过的弯路记一笔（D-38 → D-40）：先是"取安全点表第一条"（⇒ 永远边界村），
	// 再是"按配置挑最近的一条"（⇒ 永远银杏谷，确定性）；原版其实是**随机**。
	// 空候选 ⇒ 只给地图号，坐标交给 `chargen` 那边的兜底。
	if len(s.data.homePoints) > 0 {
		return chargen.PickHome(s.data.homePoints)
	}
	return chargen.Home{Map: s.data.defaultMapID}
}

// onListCharacters 列出该账号未删除的角色（选角列表）。
func (ps *protoSession) onListCharacters() bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	// ⚠️ 用**带槽位**的那份（`ListByAccountWithSlots`）：槽位按"含已删除的创建次序"
	// 编号 ⇒ 删掉一个角色后其余人留在原位（用户 2026-10-10 第 1 条）。
	slots, err := ps.srv.store.Characters().ListByAccountWithSlots(ctx, ps.rec.Account)
	if err != nil {
		log.Printf("%s: 列角色失败: %v", ps.clientIP, err)
		return ps.sendServerError(protoErrInternal, "读取角色列表失败") == nil
	}
	out := &protocol.CharacterList{}
	// 名字规则**一并下发** ⇒ 客户端预检与服务端这道门同源同措辞（D-84）
	r := chargen.NameRule()
	out.NameRule = &protocol.NameRule{
		MinBytes: r.MinBytes,
		BadChars: r.BadChars,
		Message:  r.Message,
	}
	for _, cs := range slots {
		if cs.Char == nil {
			continue
		}
		sum := characterSummary(cs.Char)
		sum.Slot = uint32(cs.Slot)
		out.Characters = append(out.Characters, sum)
	}
	log.Printf("%s: 新协议列角色 → %d 个", ps.clientIP, len(out.Characters))
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_CharacterList{
		CharacterList: out}}) == nil
}

// onSelectCharacter 选角：**这里申请角色租约**（§5）。
//
// 租约被占时**明确拒绝**（`SELECT_CHAR_LEASE_HELD`），不做"顶号"式静默踢人（D-13）。
func (ps *protoSession) onSelectCharacter(m *protocol.SelectCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	if ps.player != nil {
		// 已经在世界里：幂等回执（重复发不重复进图）。
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_OK, "", m.GetCharacterId()) == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gameLeaseWait+5*time.Second)
	defer cancel()

	chr, err := ps.findCharacter(ctx, m.GetCharacterId())
	if err != nil {
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_NOT_FOUND, err.Error(), 0) == nil
	}

	// 会话推进到"已选好角色"：`IssueGameTicket`/`ClaimGameLease` 在存储层都要求
	// `stage=4 且 character_name=本人`（见 storage/sqlite）。这一步在**新协议里
	// 本来就归 gamesvr**（D-13：它是客户端的唯一对端），只是 accountsvc 里也有一份
	// 等价的推进逻辑，将来随内部 RPC 一起收敛。
	rec := *ps.rec
	rec.Stage = sessionStageSelected
	rec.CharacterName = chr.Name
	if err := ps.srv.store.Sessions().Activate(ctx, &rec); err != nil {
		log.Printf("%s: 选角推进会话失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_SERVER_BUSY,
			"会话已失效，请重新登录", 0) == nil
	}
	ps.rec = &rec

	if err := ps.srv.store.Sessions().IssueGameTicket(ctx, rec.Account, rec.SessionID,
		chr.Name, time.Now(), time.Now().Add(protoTicketTTL)); err != nil {
		log.Printf("%s: 选角开票失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_SERVER_BUSY, "开票失败", 0) == nil
	}

	if err := ps.srv.claimGameLease(ctx, rec.Account, rec.SessionID, chr.Name); err != nil {
		log.Printf("%s: 选角申请角色租约失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_LEASE_HELD, err.Error(), 0) == nil
	}

	// 取得租约后再读一次角色：旧连接释放租约前会做最终存档，早先那份可能已经过时。
	chr, err = ps.srv.store.Characters().GetByName(ctx, chr.Name)
	if err != nil || chr.Deleted {
		_ = ps.srv.store.Sessions().ReleaseGameLease(ctx, rec.Account, rec.SessionID, chr.Name)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_NOT_FOUND, "角色不可用", 0) == nil
	}
	if err := ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_OK, "", uint64(chr.ID)); err != nil {
		return false
	}
	return ps.enterWorld(chr)
}
