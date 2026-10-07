package gamesvr

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// sendBagItems 已改为纯 ClientItem 拼接（见上方定义）。
//
// ⚠️ 早期版本用 "/\" 拼接各条目，与客户端 ClientGetBagItmes
// （按 sizeof(TClientItem) 顺序切分 body）不兼容，已废弃。
// minU32 取较小值（属性包里有几个字段要按原版夹到上限）。
func minU32(v, lim uint32) uint32 {
	if v > lim {
		return lim
	}
	return v
}

func abilityFromPB(a *pb.Ability) proto.Ability {
	if a == nil {
		return proto.Ability{Level: 1}
	}
	pack := func(m *pb.MinMax) uint32 {
		if m == nil {
			return 0
		}
		return proto.PackMinMax(uint16(m.Min), uint16(m.Max))
	}
	// ⚠️ 经验字段要**换算成"级内值"**再发：
	// 客户端经验条是 `100 * Exp / MaxExp`（FState.pas:2885），
	// 原版的 m_Abil.Exp 本来就是级内值（GetExp 升级时 Dec(Exp, MaxExp)），
	// 而我们存档里的 a.Exp 是**累计值**。直接把累计值发出去会让经验条满格/爆表，
	// 而 MaxExp 在我们这边从来没赋过值（恒 0 ⇒ 经验条除以 0）。
	return proto.Ability{
		Level: uint16(a.Level), AC: pack(a.Ac), MAC: pack(a.Mac), DC: pack(a.Dc),
		MC: pack(a.Mc), SC: pack(a.Sc), HP: uint16(a.Hp), MP: uint16(a.Mp),
		MaxHP: uint16(a.MaxHp), MaxMP: uint16(a.MaxMp),
		Exp:    uint32(entity.LevelProgress(a.Exp, a.Level)),
		MaxExp: uint32(entity.LevelNeed(a.Level)),
		// 负重（客户端负重条读的就是这几个）。原版 `m_WAbil` 由 RecalcAbilitys 写，
		// 我们由 applyWeights 写，见 weight.go。⚠️ `MaxWearWeight` 在原版还要夹到
		// `High(Byte)`=255 才发给客户端（:25066/:25086），这里照做。
		Weight:        uint16(a.Weight),
		MaxWeight:     uint16(a.MaxWeight),
		WearWeight:    uint16(a.WearWeight),
		MaxWearWeight: uint16(minU32(a.MaxWearWeight, 255)),
		HandWeight:    uint16(a.HandWeight),
		MaxHandWeight: uint16(a.MaxHandWeight),
	}
}

// sendTimeout 是单次下行包的**写超时**。
//
// ⚠️ 为什么必须有（审计 P1-6）：对方不读时，TCP 窗口填满后 `Write` 会一直阻塞
// （假死/被拔网的客户端、或只是网络拥塞），而调用点分布在世界循环、战斗广播、
// 交易等各处 —— 一个客户端就能把服务端的一条 goroutine（以及它当时持有的锁）
// 长期占住。
//
// 超时（或其它写错误）之后我们**主动断开**这条连接：
//   - TCP 上"写了一半"的流已经不可信，留着只会把后续包写花（客户端解析错乱）；
//   - 断开后读协程立刻出错退出，走正常的 removePlayer（最终存档 + 释放游戏租约）。
//
// 10 秒是"正常慢"与"已经死了"的分界：正常客户端不会 10 秒读不完一个几百字节的包。
const sendTimeout = 10 * time.Second

// send 发一个下行包。
//
// ⚠️ **c 为 nil 时静默返回**，不 panic。
// 这不是洁癖：本项目已经两次被"一个 nil 崩掉整服"教训过（坑 53/54）——
// 交易/组队这类跨玩家逻辑很容易在对方已下线的分支里拿到 nil conn，
// 而 panic 发生在每帧跑的心跳里 ⇒ 整个 gamesvr 退出、全部玩家掉线。
//
// ⚠️ 调用方**不应持有 `s.mu`**：这条路径会做真实 I/O（可能阻塞到写超时）。
// 需要的做法是"锁内收集要发的包，锁外调 send"（见 view.go 的 updateVision）。
func (s *Server) send(c net.Conn, ident uint16, recog int32, param, tag, series uint16, body string) {
	if c == nil {
		return
	}
	p := wire.Packet{Head: proto.MakeDefaultMsg(ident, recog, param, tag, series), Body: body}
	_ = c.SetWriteDeadline(time.Now().Add(sendTimeout))
	if _, err := c.Write(wire.EncodeDown(p.Encode())); err != nil {
		log.Printf("发送失败: %v", err)
		_ = c.Close() // 慢消费者断开策略：写不动就断开，让读协程去收尾
	}
}
