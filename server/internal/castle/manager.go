package castle

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// Manager 管理全部城堡（对应 TCastleManager，Castle.pas:128-155）。
//
// 单机只有一座城堡（ConfigDir="0"），但结构上保留了多城堡能力——
// 原版的 CastleList 也是这么设计的，我们只是不打算做 /castle 的多实例 UI。
//
// 并发约定：
//   - 所有对 Castle 的**读**都在锁内取指针后立刻放锁，读方法自身不加锁
//     （它们只读自身字段，由 Manager 负责不并发写同一个 Castle）；
//   - 所有**写**必须走本文件的方法（内部写锁 + 落库 + 广播）。
type Manager struct {
	store  storage.CastleStore
	guilds GuildResolver
	cfg    Config

	mu      sync.Mutex
	byDir   map[string]*Castle
	dirs    []string
	hooks   Hooks
	lastRun time.Time
}

// ErrNoCastle 表示没有找到任何城堡配置。
var ErrNoCastle = errors.New("castle: 没有可用城堡")

// NewManager 创建管理器。store 为空表示纯内存（单测用）。
func NewManager(store storage.CastleStore, guilds GuildResolver, cfg Config) *Manager {
	return &Manager{
		store:  store,
		guilds: guilds,
		cfg:    cfg,
		byDir:  make(map[string]*Castle),
	}
}

// GuildResolver 返回行会解析器（供调用方做联盟判定）。
func (m *Manager) GuildResolver() GuildResolver { return m.guilds }

// SetHooks 注册状态机副作用回调。**必须在开始 Run 之前调用。**
func (m *Manager) SetHooks(h Hooks) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = h
}

// Load 载入城堡。
//
// seeds 来自 SabukW.txt 解析（见 LoadSabukConfig）；库里已存在的记录优先
// （库里的归属/税收/宣战队列才是运行时真相，配置只提供静态坐标）。
// seeds 为空且库里也没有时，用内置默认值建一座。
func (m *Manager) Load(ctx context.Context, seeds []storage.Castle) error {
	records := make(map[string]storage.Castle, len(seeds))
	for _, s := range seeds {
		records[s.ConfigDir] = s
	}
	if m.store != nil {
		list, err := m.store.List(ctx)
		if err != nil {
			return err
		}
		for _, r := range list {
			records[r.ConfigDir] = *r
		}
	}
	if len(records) == 0 {
		records["0"] = DefaultRecord()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.byDir = make(map[string]*Castle, len(records))
	m.dirs = m.dirs[:0]
	for dir, rec := range records {
		if rec.ConfigDir == "" {
			rec.ConfigDir = dir
		}
		m.byDir[dir] = New(m.cfg, rec)
		m.dirs = append(m.dirs, dir)
	}
	sort.Strings(m.dirs)
	return nil
}

// Default 返回 ConfigDir 最小的那座城堡（单机即沙巴克）。
func (m *Manager) Default() (*Castle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dirs) == 0 {
		return nil, ErrNoCastle
	}
	return m.byDir[m.dirs[0]], nil
}

// Get 按配置子目录取城堡。
func (m *Manager) Get(dir string) (*Castle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byDir[dir]
	return c, ok
}

// All 返回全部城堡（按配置子目录有序）。
func (m *Manager) All() []*Castle {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Castle, 0, len(m.dirs))
	for _, d := range m.dirs {
		out = append(out, m.byDir[d])
	}
	return out
}

// InWarAreaOf 返回角色所在位置所属的城堡战区（无则返回 nil）。
//
// 等价原版 g_CastleManager.InCastleWarArea（Castle.pas:1301-1330）：
// 逐座城堡判 InWarArea，命中的第一座即返回。
func (m *Manager) InWarAreaOf(mapName string, x, y int) *Castle {
	for _, c := range m.All() {
		if c.InWarArea(mapName, x, y) {
			return c
		}
	}
	return nil
}

// IsCastleMember 返回该行会是否占领了某座城堡（等价 IsCastleMember，
// Castle.pas:1332-1352），无主或非占领方返回 nil。
func (m *Manager) IsCastleMember(guildName string) *Castle {
	if guildName == "" {
		return nil
	}
	for _, c := range m.All() {
		if c.IsMasterGuild(guildName) {
			return c
		}
	}
	return nil
}

// Run 推进全部城堡的状态机，并落库。
//
// tick 是两次调用的间隔，用于给城堡打上"上次跑过"的时刻，便于诊断。
func (m *Manager) Run(ctx context.Context, now time.Time) {
	castles := m.All()
	m.mu.Lock()
	h := m.hooks
	m.lastRun = now
	m.mu.Unlock()

	var dirty []*Castle
	for _, c := range castles {
		c.Run(now, h)
		if c.Dirty() {
			dirty = append(dirty, c)
		}
	}
	m.persist(ctx, dirty)
}

// persist 把变脏的城堡落库。
//
// 逐座独立保存：某座失败不影响其它座（也不会让整轮 tick 丢掉成功项）。
func (m *Manager) persist(ctx context.Context, list []*Castle) {
	if m.store == nil {
		for _, c := range list {
			c.MarkClean()
		}
		return
	}
	for _, c := range list {
		rec := c.Record()
		if err := m.store.Save(ctx, &rec); err != nil {
			log.Printf("城堡 %s 落库失败: %v", c.ConfigDir(), err)
			continue
		}
		c.MarkClean()
	}
}

// SaveNow 立刻落库全部城堡。
func (m *Manager) SaveNow(ctx context.Context) {
	m.persist(ctx, m.All())
}

// ---------- 变更入口（全部负责落库 + 副作用）----------

// AddAttacker 宣战。
func (m *Manager) AddAttacker(ctx context.Context, dir, guildName string, now time.Time) error {
	c, ok := m.Get(dir)
	if !ok {
		return ErrNoCastle
	}
	if !c.AddAttacker(guildName, now) {
		return errors.New("castle: 该行会已宣战过")
	}
	m.persist(ctx, []*Castle{c})
	return nil
}

// CancelAttacker 撤销宣战（GM 纠错用，原版无此能力）。
func (m *Manager) CancelAttacker(ctx context.Context, dir, guildName string) error {
	c, ok := m.Get(dir)
	if !ok {
		return ErrNoCastle
	}
	if !c.CancelAttacker(guildName) {
		return errors.New("castle: 该行会不在宣战队列里")
	}
	m.persist(ctx, []*Castle{c})
	return nil
}

// GetCastle 攻陷换主。返回旧占领行会名。
//
// 换主后**不**自动结束攻城：原版靠调用方判 InPalaceGuildCount <= 1
// （ObjBase.pas:6541），因为守方也在名单里，第三个行会还在就继续打。
func (m *Manager) GetCastle(ctx context.Context, dir, guildName string, now time.Time) (string, error) {
	c, ok := m.Get(dir)
	if !ok {
		return "", ErrNoCastle
	}
	old := c.GetCastle(guildName, now)
	m.persist(ctx, []*Castle{c})
	m.mu.Lock()
	h := m.hooks
	m.mu.Unlock()
	if h.OnOccupantChanged != nil {
		h.OnOccupantChanged(c, old)
	}
	return old, nil
}

// SetOccupant GM 直接指定占领方（不必在攻城期）。
func (m *Manager) SetOccupant(ctx context.Context, dir, guildName string, now time.Time) (string, error) {
	return m.GetCastle(ctx, dir, guildName, now)
}

// SetTotalGold GM 直接改金库余额。
func (m *Manager) SetTotalGold(ctx context.Context, dir string, gold int64) error {
	c, ok := m.Get(dir)
	if !ok {
		return ErrNoCastle
	}
	c.SetTotalGold(gold)
	m.persist(ctx, []*Castle{c})
	return nil
}

// IncRateGold 抽税入库，返回实际抽取额（供调用方扣减玩家所得）。
func (m *Manager) IncRateGold(ctx context.Context, dir string, gold int64) int64 {
	c, ok := m.Get(dir)
	if !ok {
		return 0
	}
	n := c.IncRateGold(gold)
	if n > 0 {
		m.persist(ctx, []*Castle{c})
	}
	return n
}

// WithDrawalGolds 掌门取款并落库。
func (m *Manager) WithDrawalGolds(ctx context.Context, dir, guildName string, isChief bool, gold int64, w Wallet) int {
	c, ok := m.Get(dir)
	if !ok {
		return GoldNoRight
	}
	code := c.WithDrawalGolds(guildName, isChief, gold, w)
	if code == GoldOK {
		m.persist(ctx, []*Castle{c})
	}
	return code
}

// ReceiptGolds 掌门存金并落库。
func (m *Manager) ReceiptGolds(ctx context.Context, dir, guildName string, isChief bool, gold int64, w Wallet) int {
	c, ok := m.Get(dir)
	if !ok {
		return GoldNoRight
	}
	code := c.ReceiptGolds(guildName, isChief, gold, w)
	if code == GoldOK {
		m.persist(ctx, []*Castle{c})
	}
	return code
}
