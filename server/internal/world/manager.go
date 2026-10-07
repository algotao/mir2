package world

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MapManager 按地图号懒加载并缓存地图。
//
// 1.76 有 605 张 .map，单张可达数 MB，全部常驻内存不现实。
// 这里按需加载 + LRU 淘汰：玩家进哪张图才加载哪张。
//
// ⚠️ 只缓存**地图静态数据**，不持有任何玩家/怪物引用——
// 淘汰时不会牵连活体实体。若将来要在 Map 上挂动态状态（门开关、
// 地面物品），必须先把那些状态迁移出去，否则淘汰即丢失。
type MapManager struct {
	dir string
	// names 是 地图号 → 显示名（来自 mapinfo.txt），可为 nil。
	names map[string]string

	mu      sync.Mutex
	maps    map[string]*Map
	lru     []string // 最近使用在前
	limit   int
	loaded  int
	evicted int

	// fileMu/files 是目录里 `.map` 文件的下标：**小写地图号 → 实际文件名**。
	fileMu sync.Mutex
	files  map[string]string
}

// NewMapManager 建立地图管理器。
//
// dir 为 .map 所在目录；limit 是同时缓存的地图数上限（<=0 表示不淘汰）。
func NewMapManager(dir string, limit int) *MapManager {
	return &MapManager{
		dir:   dir,
		maps:  make(map[string]*Map),
		limit: limit,
	}
}

// SetNames 设置地图号 → 显示名的映射（来自 mapinfo.txt）。
func (m *MapManager) SetNames(names map[string]string) {
	m.mu.Lock()
	m.names = names
	m.mu.Unlock()
}

// Name 返回地图显示名；无映射时返回地图号本身。
func (m *MapManager) Name(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.names[id]; ok && n != "" {
		return n
	}
	return id
}

// Get 取一张地图，未加载则从磁盘读取。
//
// 失败返回错误，调用方应决定是回退还是放弃切换——
// **不要**在这里静默返回空地图，否则玩家会被传送进一张假图。
func (m *MapManager) Get(id string) (*Map, error) {
	m.mu.Lock()
	if mp, ok := m.maps[id]; ok {
		m.touch(id)
		m.mu.Unlock()
		return mp, nil
	}
	m.mu.Unlock()

	// 加载在锁外进行：读几 MB 的文件不该阻塞其它地图的访问
	path, err := m.mapFile(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取地图 %s: %w", id, err)
	}
	mp, err := Parse(id, raw)
	if err != nil {
		return nil, fmt.Errorf("解析地图 %s: %w", id, err)
	}

	m.mu.Lock()
	// 双重检查：等待期间可能已被别的 goroutine 加载
	if exist, ok := m.maps[id]; ok {
		m.touch(id)
		m.mu.Unlock()
		return exist, nil
	}
	m.maps[id] = mp
	m.lru = append([]string{id}, m.lru...)
	m.loaded++
	m.evictLocked()
	m.mu.Unlock()
	return mp, nil
}

// mapFile 返回地图号对应的磁盘路径。
//
// ⚠️ **不能**直接拼 `<id>.map`：官方 mapinfo.txt 与 .map 文件的**大小写
// 在上百处对不上**（mapinfo 写 `D421`、文件却是 `d421.map`；实测 329 条里
// 有 35 条如此）。原版服务端跑在 Windows 上、文件系统大小写不敏感，所以
// 从没暴露；换到 Linux 后精确拼路径会直接「地图不存在」。
// 这里先试精确名，再退回**按小写建索引**的查找，还原 Windows 的语义。
func (m *MapManager) mapFile(id string) (string, error) {
	exact := filepath.Join(m.dir, id+".map")
	if _, err := os.Stat(exact); err == nil {
		return exact, nil
	}
	name, err := m.lookupCaseInsensitive(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.dir, name), nil
}

// lookupCaseInsensitive 在小写索引里找 id（索引只建一次）。
func (m *MapManager) lookupCaseInsensitive(id string) (string, error) {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()
	if m.files == nil {
		files := make(map[string]string)
		ents, err := os.ReadDir(m.dir)
		if err != nil {
			return "", fmt.Errorf("读取地图目录 %s: %w", m.dir, err)
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			ext := filepath.Ext(e.Name())
			if !strings.EqualFold(ext, ".map") {
				continue
			}
			base := strings.TrimSuffix(e.Name(), ext)
			files[strings.ToLower(base)] = e.Name()
		}
		m.files = files
	}
	name, ok := m.files[strings.ToLower(id)]
	if !ok {
		return "", fmt.Errorf("地图 %s 不存在（目录 %s）", id, m.dir)
	}
	return name, nil
}

// touch 把 id 移到 LRU 头部。**调用方持锁**。
func (m *MapManager) touch(id string) {
	for i, v := range m.lru {
		if v == id {
			copy(m.lru[i:], m.lru[i+1:])
			m.lru = m.lru[:len(m.lru)-1]
			break
		}
	}
	m.lru = append([]string{id}, m.lru...)
}

// evictLocked 淘汰超出上限的地图。**调用方持锁**。
func (m *MapManager) evictLocked() {
	if m.limit <= 0 || len(m.lru) <= m.limit {
		return
	}
	for len(m.lru) > m.limit {
		oldest := m.lru[len(m.lru)-1]
		m.lru = m.lru[:len(m.lru)-1]
		delete(m.maps, oldest)
		m.evicted++
	}
}

// Put 直接放入一张地图（用于预置默认地图或注入程序生成图）。
func (m *MapManager) Put(mp *Map) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.maps[mp.Name]; ok {
		return
	}
	m.maps[mp.Name] = mp
	m.lru = append([]string{mp.Name}, m.lru...)
	m.evictLocked()
}

// Stats 返回加载统计。
func (m *MapManager) Stats() (cached, loaded, evicted int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.maps), m.loaded, m.evicted
}
