package guild

import (
	"context"
	"errors"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// 管理器错误。
var (
	// ErrNotFound 表示行会不存在。
	ErrNotFound = errors.New("guild: 行会不存在")
	// ErrExists 表示同名行会已存在。
	ErrExists = errors.New("guild: 行会已存在")
	// ErrAlreadyInGuild 表示该角色已在其它行会。
	ErrAlreadyInGuild = errors.New("guild: 角色已在行会中")
)

// Manager 是行会的内存缓存与持久化入口。
//
// 对应原版 TGuildManager（Guild.pas:93-107）：启动时全量载入，
// 按成员名反查行会（MemberOfGuild）。所有变更立即落库
// （原版 UpdateGuildFile 也是立即写盘，Guild.pas:801-806）。
//
// 并发约定：行会数据只能经 Manager 读写；Find 等方法返回**副本**，
// 调用方拿到的指针可以自由使用，不会与缓存共享状态。
type Manager struct {
	store storage.GuildStore

	mu       sync.RWMutex
	byName   map[string]*storage.Guild
	byMember map[string]string // 成员角色名 → 行会名
}

// NewManager 创建管理器。
func NewManager(store storage.GuildStore) *Manager {
	return &Manager{
		store:    store,
		byName:   make(map[string]*storage.Guild),
		byMember: make(map[string]string),
	}
}

// Load 从存储全量载入。
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byName = make(map[string]*storage.Guild, len(list))
	m.byMember = make(map[string]string)
	for _, g := range list {
		m.indexOf(g)
	}
	return nil
}

// Find 按行会名取副本；不存在返回 nil。
func (m *Manager) Find(name string) *storage.Guild {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Clone(m.byName[name])
}

// OfMember 按成员名反查行会（等价原版 MemberOfGuild），
// 同时返回该成员的职务号与职务名；未入会时全部为零值。
func (m *Manager) OfMember(member string) (*storage.Guild, int, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g := m.byName[m.byMember[member]]
	if g == nil {
		return nil, 0, ""
	}
	c := Clone(g)
	return c, RankNoOf(c, member), RankNameOf(c, member)
}

// Names 返回全部行会名（有序）。
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.byName))
	for name := range m.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Count 返回行会数。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byName)
}

// Roster 返回行会全部成员名（有序，便于测试与遍历在线对象）。
func (m *Manager) Roster(name string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g := m.byName[name]
	if g == nil {
		return nil
	}
	out := make([]string, 0, Count(g))
	for _, r := range g.Ranks {
		out = append(out, r.Members...)
	}
	return out
}

// Create 建会：掌门入 rank 1（等价原版 AddGuild + SetGuildInfo）。
//
// 行会名合法性（长度/字符）由调用方校验，见 cmd/gamesvr 的 checkGuildName。
func (m *Manager) Create(ctx context.Context, name, chief string) (*storage.Guild, error) {
	m.mu.Lock()
	if _, ok := m.byName[name]; ok {
		m.mu.Unlock()
		return nil, ErrExists
	}
	if _, ok := m.byMember[chief]; ok {
		m.mu.Unlock()
		return nil, ErrAlreadyInGuild
	}
	g := New(name, chief)
	m.mu.Unlock()

	if err := m.store.Create(ctx, g); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.commit(g)
	m.mu.Unlock()
	return Clone(g), nil
}

// Delete 解散行会（等价原版 TGuildManager.DELGUILD）。
func (m *Manager) Delete(ctx context.Context, name string) error {
	m.mu.Lock()
	g := m.byName[name]
	if g == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	m.mu.Unlock()

	if err := m.store.Delete(ctx, name); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.byName, name)
	for _, r := range g.Ranks {
		for _, mem := range r.Members {
			delete(m.byMember, mem)
		}
	}
	m.mu.Unlock()
	return nil
}

// AddMember 把角色加入默认成员职务（等价原版 TGUild.AddMember；
// 入会条件由调用方校验，见 cmd/gamesvr 的 handleGuildAddMember）。
func (m *Manager) AddMember(ctx context.Context, guildName, member string) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		if IsMember(g, member) {
			return ErrAlreadyInGuild
		}
		AddMember(g, member)
		return nil
	})
}

// DelMember 把角色移出行会。返回 false 表示其不在行会。
func (m *Manager) DelMember(ctx context.Context, guildName, member string) (bool, error) {
	var removed bool
	err := m.mutate(ctx, guildName, func(g *storage.Guild) error {
		removed = DelMember(g, member)
		return nil
	})
	return removed, err
}

// SetNotice 替换行会公告（等价原版 CM_GUILDUPDATENOTICE 的写入部分）。
func (m *Manager) SetNotice(ctx context.Context, guildName string, lines []string) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		g.Notice = lines
		return nil
	})
}

// SetEnableAuthAlly 设置"允许被结盟"开关（@AuthAlly）。
func (m *Manager) SetEnableAuthAlly(ctx context.Context, guildName string, v bool) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		g.EnableAuthAlly = v
		return nil
	})
}

// ---------- 行会争霸赛 ----------
//
// 都走 mutate（改副本 → 落库 → 提交内存）：原版这几处也是"改完立即写盘"
// （UpdateGuildFile，Guild.pas:801-806）。

// StartTeamFight 开始争霸赛（`@StartContest` 对每个参战行会调一次）。
func (m *Manager) StartTeamFight(ctx context.Context, guildName string) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		StartTeamFight(g)
		return nil
	})
}

// EndTeamFight 结束争霸赛（**保留**积分与成员表，供 `@EndContest` 广播最终比分）。
func (m *Manager) EndTeamFight(ctx context.Context, guildName string) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		EndTeamFight(g)
		return nil
	})
}

// AddTeamFightMember 登记一名参赛成员。
func (m *Manager) AddTeamFightMember(ctx context.Context, guildName, member string) error {
	return m.mutate(ctx, guildName, func(g *storage.Guild) error {
		AddTeamFightMember(g, member)
		return nil
	})
}

// TeamFightWhoDead 记一次阵亡（返回是否真的记了：争霸赛没开或不在表里就不记）。
func (m *Manager) TeamFightWhoDead(ctx context.Context, guildName, member string) (bool, error) {
	hit := false
	err := m.mutate(ctx, guildName, func(g *storage.Guild) error {
		hit = TeamFightWhoDead(g, member)
		return nil
	})
	return hit, err
}

// TeamFightWhoWinPoint 给击杀方行会与击杀者个人加分。
//
// ⚠️ 调用方要先确认该行会 `TeamFight` 开着再调：mutate 无论如何都会写一次库。
func (m *Manager) TeamFightWhoWinPoint(ctx context.Context, guildName, member string, points int) (bool, error) {
	hit := false
	err := m.mutate(ctx, guildName, func(g *storage.Guild) error {
		hit = TeamFightWhoWinPoint(g, member, points)
		return nil
	})
	return hit, err
}

// UpdateRanks 用客户端提交的职务表文本更新职务
// （等价原版 TGUild.UpdateRank，Guild.pas:911-1174）。
//
// 返回原版返回码（RankOK / RankNoChange / RankBadChief / ...）。
// online 用于 -5 检查，nil 表示不检查。
func (m *Manager) UpdateRanks(ctx context.Context, guildName, text string, online func(string) bool) (int, error) {
	m.mu.Lock()
	cur := m.byName[guildName]
	if cur == nil {
		m.mu.Unlock()
		return 0, ErrNotFound
	}
	ranks := ParseRankData(text)
	if code := ValidateRanks(cur, ranks, online); code != RankOK {
		m.mu.Unlock()
		return code, nil
	}
	ng := Clone(cur)
	ng.Ranks = ranks
	m.mu.Unlock()

	if err := m.store.Save(ctx, ng); err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.commit(ng)
	m.mu.Unlock()
	return RankOK, nil
}

// AddAlly 登记双向联盟（原版在两处分别 AllyGuild，见 ObjBase.pas:18217-18268）。
//
// ⚠️ 两条记录分别落库，不是原子操作；中途失败会留下单向联盟，
// 下次结盟会因 HasAlly 去重而自愈。
func (m *Manager) AddAlly(ctx context.Context, a, b string) error {
	if err := m.mutate(ctx, a, func(g *storage.Guild) error { AddAlly(g, b); return nil }); err != nil {
		return err
	}
	return m.mutate(ctx, b, func(g *storage.Guild) error { AddAlly(g, a); return nil })
}

// BreakAlly 解除双向联盟，返回是否确实解除（以 a 侧为准，同原版）。
func (m *Manager) BreakAlly(ctx context.Context, a, b string) (bool, error) {
	var had bool
	if err := m.mutate(ctx, a, func(g *storage.Guild) error { had = DelAlly(g, b); return nil }); err != nil {
		return false, err
	}
	if !had {
		return false, nil
	}
	if err := m.mutate(ctx, b, func(g *storage.Guild) error { DelAlly(g, a); return nil }); err != nil {
		return true, err
	}
	return true, nil
}

// AddWar 把敌对行会写入双方的行会战列表（等价原版 TPlayObject.ReQuestGuildWar，
// ObjBase.pas:26722-26762），到期时刻由调用方按价格换算。
func (m *Manager) AddWar(ctx context.Context, a, b string, until time.Time) error {
	add := func(g *storage.Guild, enemy string) {
		for i := range g.Wars {
			if g.Wars[i].Name == enemy {
				g.Wars[i].EndAt = until
				return
			}
		}
		g.Wars = append(g.Wars, storage.GuildWar{Name: enemy, EndAt: until})
	}
	if err := m.mutate(ctx, a, func(g *storage.Guild) error { add(g, b); return nil }); err != nil {
		return err
	}
	return m.mutate(ctx, b, func(g *storage.Guild) error { add(g, a); return nil })
}

// RemoveWar 撤销一条战争记录（双向）。
//
// 用于宣战失败的回滚：原版 ReQuestGuildWar 先给己方 AddWarGuild，
// 对方失败时把 dwWarTick 置 0 回滚（ObjBase.pas:26722-26748）。
func (m *Manager) RemoveWar(ctx context.Context, a, b string) error {
	drop := func(g *storage.Guild, enemy string) {
		g.Wars = slices.DeleteFunc(g.Wars, func(w storage.GuildWar) bool {
			return w.Name == enemy
		})
	}
	if err := m.mutate(ctx, a, func(g *storage.Guild) error { drop(g, b); return nil }); err != nil {
		return err
	}
	return m.mutate(ctx, b, func(g *storage.Guild) error { drop(g, a); return nil })
}

// PruneWars 清掉已到期的战争记录，返回被清理的（行会, 敌对行会）对。
//
// 原版靠 dwWarTick 递减（Guild.pas:614）自然结束；我们存绝对到期时刻，
// 到期后在查询/展示前剔除，等价效果且跨重启不漂移。
func (m *Manager) PruneWars(ctx context.Context, now time.Time) [][2]string {
	m.mu.Lock()
	// 战争是双向记录的（A 的敌人是 B，B 的敌人也是 A），
	// 所以同一对要去重，返回的每一对只出现一次。
	seen := make(map[[2]string]bool)
	var dropped [][2]string
	for _, g := range m.byName {
		for _, w := range g.Wars {
			if w.EndAt.After(now) {
				continue
			}
			pair := [2]string{g.Name, w.Name}
			rev := [2]string{w.Name, g.Name}
			if seen[pair] || seen[rev] {
				continue
			}
			seen[pair] = true
			dropped = append(dropped, pair)
		}
	}
	m.mu.Unlock()
	for _, d := range dropped {
		if err := m.RemoveWar(ctx, d[0], d[1]); err != nil {
			log.Printf("清理行会战 %s vs %s 失败: %v", d[0], d[1], err)
		}
	}
	return dropped
}

// ActiveWars 返回 g 当前有效的敌对行会名（已剔除过期项）。
func (m *Manager) ActiveWars(g *storage.Guild, now time.Time) []string {
	if g == nil {
		return nil
	}
	var out []string
	for _, w := range g.Wars {
		if w.EndAt.After(now) {
			out = append(out, w.Name)
		}
	}
	return out
}

// mutate 是行会变更的统一入口：
// 锁内取副本 → 副本上校验并修改 → 锁外落库 → 锁内提交。
//
// ⚠️ 落库失败时内存保持原样（副本被丢弃），不存在"内存已改、库里没改"的窗口。
func (m *Manager) mutate(ctx context.Context, guildName string, fn func(g *storage.Guild) error) error {
	m.mu.Lock()
	cur := m.byName[guildName]
	if cur == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	ng := Clone(cur)
	if err := fn(ng); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	if err := m.store.Save(ctx, ng); err != nil {
		return err
	}

	m.mu.Lock()
	m.commit(ng)
	m.mu.Unlock()
	return nil
}

// commit 把新版本行会写入缓存并重建其成员索引。调用方必须持有写锁。
func (m *Manager) commit(g *storage.Guild) {
	if old := m.byName[g.Name]; old != nil {
		for _, r := range old.Ranks {
			for _, mem := range r.Members {
				delete(m.byMember, mem)
			}
		}
	}
	m.indexOf(g)
}

// indexOf 建立索引。调用方必须持有写锁。
func (m *Manager) indexOf(g *storage.Guild) {
	m.byName[g.Name] = g
	for _, r := range g.Ranks {
		for _, mem := range r.Members {
			m.byMember[mem] = g.Name
		}
	}
}
