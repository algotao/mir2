// Package sqlite 是 storage.Store 的 SQLite 实现（默认后端）。
//
// 驱动选用 modernc.org/sqlite（纯 Go、无 CGO）——CGO 会同时破坏两件事：
//   - 容器化：需要带 glibc 的基础镜像，无法用 distroless/scratch
//   - 多平台：交叉编译要配目标平台工具链
//
// 见 docs/service-architecture.md 与 docs/reference-projects.md。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver name: "sqlite"

	"google.golang.org/protobuf/proto"

	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

const driverName = "sqlite"

// Options 是打开数据库的参数。
type Options struct {
	// MaxOpenConns 连接池上限。SQLite 是单写者，默认 1 完全串行化，
	// 可彻底避免 "database is locked"。数据本身有内存缓存，DB 访问不频繁，
	// 串行不会成为瓶颈。若确需并发读，可调大并依赖 busy_timeout。
	MaxOpenConns int
	// BusyTimeoutMs 遇锁时的等待毫秒数。
	BusyTimeoutMs int
}

// DefaultOptions 返回默认参数。
func DefaultOptions() Options {
	return Options{MaxOpenConns: 1, BusyTimeoutMs: 5000}
}

// Store 是 SQLite 后端。
type Store struct {
	db       *sql.DB
	accounts *accountStore
	chars    *characterStore
	sessions *sessionStore
	guilds   *guildStore
	castles  *castleStore
}

// Open 以默认参数打开数据库。
func Open(path string) (*Store, error) { return OpenWithOptions(path, DefaultOptions()) }

// OpenWithOptions 打开数据库并建表。
func OpenWithOptions(path string, opt Options) (*Store, error) {
	if opt.MaxOpenConns <= 0 {
		opt.MaxOpenConns = DefaultOptions().MaxOpenConns
	}
	if opt.BusyTimeoutMs <= 0 {
		opt.BusyTimeoutMs = DefaultOptions().BusyTimeoutMs
	}

	db, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开 %s: %w", path, err)
	}
	db.SetMaxOpenConns(opt.MaxOpenConns)

	if err := applyPragmas(db, opt.BusyTimeoutMs); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db}
	s.accounts = &accountStore{db: db}
	s.chars = &characterStore{db: db}
	s.sessions = &sessionStore{db: db}
	s.guilds = &guildStore{db: db}
	s.castles = &castleStore{db: db}
	return s, nil
}

// applyPragmas 设置 SQLite 运行参数。
//
// ⚠️ busy_timeout 必须**最先**设置：切换 journal_mode 需要排他锁，
// 若此时 busy_timeout 仍是 0，多进程同时打开同一个库会直接 SQLITE_BUSY 失败
// （实测 accountsvc 与 gamesvr 同时启动时必现）。
//
// WAL 是关键：它让读写不互斥，避免"玩家正在操作时触发存档"卡住读请求。
func applyPragmas(db *sql.DB, busyMs int) error {
	// 1. 先设超时，后续所有可能争锁的操作才有等待余地
	if _, err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", busyMs)); err != nil {
		return fmt.Errorf("sqlite: 设置 busy_timeout: %w", err)
	}

	// 2. 切换 WAL。
	//
	// ⚠️ PRAGMA journal_mode **不受 busy_timeout 保护**（SQLite 已知行为），
	// 多进程同时首次打开同一个库时必有一个拿到 SQLITE_BUSY。
	// 因此需要重试；而 WAL 只是性能优化、不是正确性要求，
	// 最终失败也只告警、不阻断启动（否则后启动的服务会直接退出）。
	var mode string
	for i := 0; i < 3; i++ {
		if _, err := db.Exec("PRAGMA journal_mode = WAL"); err == nil {
			break
		}
		time.Sleep(time.Duration(50*(i+1)) * time.Millisecond)
	}
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil ||
		!strings.EqualFold(mode, "wal") {
		log.Printf("sqlite: 警告 —— 未能启用 WAL（当前 %q），并发读写性能会下降", mode)
	}

	for _, p := range []string{"PRAGMA synchronous = NORMAL", "PRAGMA foreign_keys = ON"} {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("sqlite: 执行 %q: %w", p, err)
		}
	}
	return nil
}

// schema 是建表语句。
//
// ⚠️ 部署注意：SQLite 的文件锁在 NFS 及部分 CSI driver 上不可靠，
// 必须放在本地卷，否则会出现间歇性 "database is locked"。
const schema = `
CREATE TABLE IF NOT EXISTS accounts (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    password_hash BLOB    NOT NULL,
    salt          BLOB    NOT NULL,
    error_count   INTEGER NOT NULL DEFAULT 0,
    action_tick   INTEGER NOT NULL DEFAULT 0,
    deleted       INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    data          BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_accounts_deleted ON accounts(deleted);

CREATE TABLE IF NOT EXISTS characters (
    id         INTEGER PRIMARY KEY,
    account    TEXT    NOT NULL,
    name       TEXT    NOT NULL UNIQUE,
    job        INTEGER NOT NULL DEFAULT 0,
    level      INTEGER NOT NULL DEFAULT 1,
    gold       INTEGER NOT NULL DEFAULT 0,
    deleted    INTEGER NOT NULL DEFAULT 0,
    last_login INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    data       BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_characters_account ON characters(account, deleted);

CREATE TABLE IF NOT EXISTS sessions (
    session_id      INTEGER PRIMARY KEY,
    account         TEXT    NOT NULL DEFAULT '',
    ip              TEXT    NOT NULL DEFAULT '',
    stage           INTEGER NOT NULL DEFAULT 0,
    server_name     TEXT    NOT NULL DEFAULT '',
    character_name  TEXT    NOT NULL DEFAULT '',
    handoff_used    INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_account ON sessions(account);
CREATE TABLE IF NOT EXISTS account_sessions (
    account     TEXT    PRIMARY KEY,
    session_id  INTEGER NOT NULL UNIQUE,
    expires_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS game_leases (
    account         TEXT    NOT NULL,
    session_id      INTEGER NOT NULL,
    character_name  TEXT    NOT NULL,
    expires_at      INTEGER NOT NULL,
    PRIMARY KEY (account, character_name)
);
CREATE INDEX IF NOT EXISTS idx_game_leases_session ON game_leases(session_id);
CREATE TABLE IF NOT EXISTS game_tickets (
    session_id      INTEGER NOT NULL,
    character_name  TEXT    NOT NULL,
    expires_at      INTEGER NOT NULL,
    consumed        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, character_name)
);

CREATE TABLE IF NOT EXISTS guilds (
    name       TEXT    PRIMARY KEY,
    data       BLOB    NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS castles (
    config_dir TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL DEFAULT '',
    data       BLOB    NOT NULL,
    updated_at INTEGER NOT NULL
);
`

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("sqlite: 建表: %w", err)
	}
	// Upgrade databases created before session ownership fields were introduced.
	for name, definition := range map[string]string{
		"character_name": "TEXT NOT NULL DEFAULT ''",
		"handoff_used":   "INTEGER NOT NULL DEFAULT 0",
		"expires_at":     "INTEGER NOT NULL DEFAULT 0",
	} {
		rows, err := db.Query("PRAGMA table_info(sessions)")
		if err != nil {
			return fmt.Errorf("sqlite: 检查 sessions.%s: %w", name, err)
		}
		found := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var column, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("sqlite: 读取 sessions 列: %w", err)
			}
			if column == name {
				found = true
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite: 遍历 sessions 列: %w", err)
		}
		_ = rows.Close()
		if !found {
			if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN " + name + " " + definition); err != nil &&
				!strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("sqlite: 升级 sessions.%s: %w", name, err)
			}
		}
	}
	if err := migrateGameLeases(db); err != nil {
		return err
	}
	return nil
}

func migrateGameLeases(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(game_leases)")
	if err != nil {
		return fmt.Errorf("sqlite: 检查 game_leases 主键: %w", err)
	}
	accountPK, characterPK := 0, 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var column, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite: 读取 game_leases 列: %w", err)
		}
		switch column {
		case "account":
			accountPK = primaryKey
		case "character_name":
			characterPK = primaryKey
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("sqlite: 遍历 game_leases 列: %w", err)
	}
	_ = rows.Close()
	if accountPK != 1 || characterPK != 0 {
		return nil
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE game_leases RENAME TO game_leases_account_key`,
		`CREATE TABLE game_leases (
		 account TEXT NOT NULL, session_id INTEGER NOT NULL, character_name TEXT NOT NULL,
		 expires_at INTEGER NOT NULL, PRIMARY KEY(account, character_name))`,
		`INSERT INTO game_leases(account, session_id, character_name, expires_at)
		 SELECT account, session_id, character_name, expires_at FROM game_leases_account_key`,
		`DROP TABLE game_leases_account_key`,
		`CREATE INDEX IF NOT EXISTS idx_game_leases_session ON game_leases(session_id)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("sqlite: 迁移 game_leases: %w", err)
		}
	}
	return mapErr(tx.Commit())
}

// Accounts 返回账号存储.
func (s *Store) Accounts() storage.AccountStore { return s.accounts }

// Characters 返回角色存储。
func (s *Store) Characters() storage.CharacterStore { return s.chars }

// Sessions 返回会话存储。
func (s *Store) Sessions() storage.SessionStore { return s.sessions }

// Guilds 返回行会存储。
func (s *Store) Guilds() storage.GuildStore { return s.guilds }

// Castles 返回城堡存储。
func (s *Store) Castles() storage.CastleStore { return s.castles }

// Ping 检查连接可用。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close 关闭数据库。
//
// ⚠️ 必须调用：进程被强杀时 WAL 未 checkpoint，虽然不会丢已提交事务，
// 但下次启动要做恢复。优雅退出（SIGTERM）时应显式 Close。
func (s *Store) Close() error { return s.db.Close() }

// ---------- 错误映射 ----------

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	// modernc.org/sqlite 未导出错误码类型，按文案判定唯一键冲突。
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return storage.ErrExists
	}
	return err
}

func isNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }

// ---------- 账号 ----------

type accountStore struct{ db *sql.DB }

func (s *accountStore) Create(ctx context.Context, a *storage.Account) error {
	data, err := marshalAccount(a.Data)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts
		 (name, password_hash, salt, error_count, action_tick, deleted, created_at, updated_at, data)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		a.Name, a.PasswordHash, a.Salt, a.ErrorCount, a.ActionTick,
		boolToInt(a.Deleted), now, now, data)
	if err != nil {
		return mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	a.ID = id
	a.CreatedAt = time.Unix(now, 0)
	a.UpdatedAt = a.CreatedAt
	return nil
}

func (s *accountStore) GetByName(ctx context.Context, name string) (*storage.Account, error) {
	const q = `SELECT id, name, password_hash, salt, error_count, action_tick,
	                  deleted, created_at, updated_at, data
	           FROM accounts WHERE name = ?`
	var (
		a         storage.Account
		deleted   int
		createdAt int64
		updatedAt int64
		data      []byte
	)
	err := s.db.QueryRowContext(ctx, q, name).Scan(
		&a.ID, &a.Name, &a.PasswordHash, &a.Salt, &a.ErrorCount, &a.ActionTick,
		&deleted, &createdAt, &updatedAt, &data)
	if err != nil {
		return nil, mapErr(err)
	}
	a.Deleted = deleted != 0
	a.CreatedAt = time.Unix(createdAt, 0)
	a.UpdatedAt = time.Unix(updatedAt, 0)
	if a.Data, err = unmarshalAccount(data); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *accountStore) Update(ctx context.Context, a *storage.Account) error {
	data, err := marshalAccount(a.Data)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET password_hash=?, salt=?, error_count=?, action_tick=?,
		 deleted=?, updated_at=?, data=? WHERE id=?`,
		a.PasswordHash, a.Salt, a.ErrorCount, a.ActionTick,
		boolToInt(a.Deleted), now, data, a.ID)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

func (s *accountStore) MarkDeleted(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET deleted=1, updated_at=? WHERE id=?`, time.Now().Unix(), id)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

// ---------- 角色 ----------

type characterStore struct{ db *sql.DB }

func (s *characterStore) Create(ctx context.Context, c *storage.Character) error {
	c.SyncFromData()
	data, err := marshalCharacter(c.Data)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO characters
		 (account, name, job, level, gold, deleted, last_login, created_at, updated_at, data)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.Account, c.Name, c.Job, c.Level, c.Gold,
		boolToInt(c.Deleted), now, now, now, data)
	if err != nil {
		return mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	c.ID = id
	c.CreatedAt = time.Unix(now, 0)
	c.UpdatedAt = c.CreatedAt
	c.LastLogin = c.CreatedAt
	return nil
}

func (s *characterStore) GetByName(ctx context.Context, name string) (*storage.Character, error) {
	const q = `SELECT id, account, name, job, level, gold, deleted,
	                  last_login, created_at, updated_at, data
	           FROM characters WHERE name = ?`
	var (
		c         storage.Character
		deleted   int
		lastLogin int64
		createdAt int64
		updatedAt int64
		data      []byte
	)
	err := s.db.QueryRowContext(ctx, q, name).Scan(
		&c.ID, &c.Account, &c.Name, &c.Job, &c.Level, &c.Gold,
		&deleted, &lastLogin, &createdAt, &updatedAt, &data)
	if err != nil {
		return nil, mapErr(err)
	}
	c.Deleted = deleted != 0
	c.LastLogin = time.Unix(lastLogin, 0)
	c.CreatedAt = time.Unix(createdAt, 0)
	c.UpdatedAt = time.Unix(updatedAt, 0)
	if c.Data, err = unmarshalCharacter(data); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *characterStore) ListByAccount(ctx context.Context, account string) ([]*storage.Character, error) {
	const q = `SELECT id, account, name, job, level, gold, deleted,
	                  last_login, created_at, updated_at, data
	           FROM characters WHERE account = ? AND deleted = 0 ORDER BY id`
	rows, err := s.db.QueryContext(ctx, q, account)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*storage.Character
	for rows.Next() {
		var (
			c         storage.Character
			deleted   int
			lastLogin int64
			createdAt int64
			updatedAt int64
			data      []byte
		)
		if err := rows.Scan(&c.ID, &c.Account, &c.Name, &c.Job, &c.Level, &c.Gold,
			&deleted, &lastLogin, &createdAt, &updatedAt, &data); err != nil {
			return nil, err
		}
		c.Deleted = deleted != 0
		c.LastLogin = time.Unix(lastLogin, 0)
		c.CreatedAt = time.Unix(createdAt, 0)
		c.UpdatedAt = time.Unix(updatedAt, 0)
		if c.Data, err = unmarshalCharacter(data); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *characterStore) Update(ctx context.Context, c *storage.Character) error {
	c.SyncFromData()
	data, err := marshalCharacter(c.Data)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE characters SET account=?, name=?, job=?, level=?, gold=?,
		 deleted=?, last_login=?, updated_at=?, data=? WHERE id=?`,
		c.Account, c.Name, c.Job, c.Level, c.Gold,
		boolToInt(c.Deleted), c.LastLogin.Unix(), now, data, c.ID)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

// UpdateForSession fences an old game's save after another session has acquired the account lease.
func (s *characterStore) UpdateForSession(ctx context.Context, c *storage.Character, sessionID int32) error {
	c.SyncFromData()
	data, err := marshalCharacter(c.Data)
	if err != nil {
		return err
	}
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE characters SET account=?, name=?, job=?, level=?, gold=?, deleted=?,
		 last_login=?, updated_at=?, data=? WHERE id=? AND EXISTS (
		   SELECT 1 FROM game_leases g WHERE g.account=? AND g.session_id=?
		     AND g.character_name=? AND g.expires_at>?)`,
		c.Account, c.Name, c.Job, c.Level, c.Gold, boolToInt(c.Deleted),
		c.LastLogin.Unix(), now.Unix(), data, c.ID, c.Account, sessionID, c.Name, now.UnixMilli())
	if err != nil {
		return mapErr(err)
	}
	if err := expectOne(res); err != nil {
		return storage.ErrLeaseLost
	}
	return nil
}

func (s *characterStore) MarkDeleted(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE characters SET deleted=1, updated_at=? WHERE id=?`, time.Now().Unix(), id)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

// ---------- 会话 ----------

type sessionStore struct{ db *sql.DB }

func (s *sessionStore) Create(ctx context.Context, r *storage.SessionRecord) error {
	now := time.Now()
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = now.Add(10 * time.Minute)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (session_id, account, ip, stage, server_name, character_name,
		 handoff_used, expires_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Account, r.IP, r.Stage, r.ServerName, r.CharacterName,
		boolToInt(r.HandoffUsed), r.ExpiresAt.UnixMilli(), now.Unix(), now.Unix())
	if err != nil {
		return mapErr(err)
	}
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

func (s *sessionStore) Get(ctx context.Context, sessionID int32) (*storage.SessionRecord, error) {
	const q = `SELECT session_id, account, ip, stage, server_name, character_name, handoff_used,
	                  expires_at, created_at, updated_at
	           FROM sessions WHERE session_id = ?`
	var (
		r         storage.SessionRecord
		handoff   int
		expiresAt int64
		createdAt int64
		updatedAt int64
	)
	err := s.db.QueryRowContext(ctx, q, sessionID).Scan(
		&r.SessionID, &r.Account, &r.IP, &r.Stage, &r.ServerName, &r.CharacterName,
		&handoff, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	r.HandoffUsed = handoff != 0
	r.ExpiresAt = time.UnixMilli(expiresAt)
	r.CreatedAt = time.Unix(createdAt, 0)
	r.UpdatedAt = time.Unix(updatedAt, 0)
	return &r, nil
}

// Activate atomically switches the account's current login lease to this session.
func (s *sessionStore) Activate(ctx context.Context, r *storage.SessionRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	now := time.Now()
	if r.ExpiresAt.IsZero() || r.ExpiresAt.Before(now) {
		r.ExpiresAt = now.Add(10 * time.Minute)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET account=?, stage=?, server_name=?, character_name=?, handoff_used=0,
		 expires_at=?, updated_at=? WHERE session_id=? AND expires_at>?`,
		r.Account, r.Stage, r.ServerName, r.CharacterName, r.ExpiresAt.UnixMilli(),
		now.Unix(), r.SessionID, now.UnixMilli())
	if err != nil {
		return mapErr(err)
	}
	if err := expectOne(res); err != nil {
		return storage.ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account_sessions(account, session_id, expires_at) VALUES(?,?,?)
		 ON CONFLICT(account) DO UPDATE SET session_id=excluded.session_id, expires_at=excluded.expires_at`,
		r.Account, r.SessionID, r.ExpiresAt.UnixMilli()); err != nil {
		return mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return mapErr(err)
	}
	r.UpdatedAt = now
	return nil
}

func (s *sessionStore) Update(ctx context.Context, r *storage.SessionRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	now := time.Now()
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = now.Add(10 * time.Minute)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET account=?, ip=?, stage=?, server_name=?, character_name=?, expires_at=?, updated_at=?
		 WHERE session_id=? AND account=? AND EXISTS (
		   SELECT 1 FROM account_sessions a WHERE a.account=? AND a.session_id=? AND a.expires_at>?)`,
		r.Account, r.IP, r.Stage, r.ServerName, r.CharacterName, r.ExpiresAt.UnixMilli(), now.Unix(),
		r.SessionID, r.Account, r.Account, r.SessionID, now.UnixMilli())
	if err != nil {
		return mapErr(err)
	}
	if err := expectOne(res); err != nil {
		return storage.ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE account_sessions SET expires_at=? WHERE account=? AND session_id=?`,
		r.ExpiresAt.UnixMilli(), r.Account, r.SessionID); err != nil {
		return mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return mapErr(err)
	}
	r.UpdatedAt = now
	return nil
}

func (s *sessionStore) IsCurrent(ctx context.Context, account string, sessionID int32, now time.Time) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM account_sessions a JOIN sessions s ON s.session_id=a.session_id
		 WHERE a.account=? AND a.session_id=? AND a.expires_at>? AND s.account=? AND s.expires_at>?)`,
		account, sessionID, now.UnixMilli(), account, now.UnixMilli()).Scan(&found)
	return found != 0, mapErr(err)
}

func (s *sessionStore) BindHandoff(ctx context.Context, account string, sessionID int32, now time.Time) (*storage.SessionRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET handoff_used=1, updated_at=?
		 WHERE session_id=? AND account=? AND stage>=2 AND handoff_used=0 AND expires_at>? AND EXISTS (
		   SELECT 1 FROM account_sessions a WHERE a.account=? AND a.session_id=? AND a.expires_at>?)`,
		now.Unix(), sessionID, account, now.UnixMilli(), account, sessionID, now.UnixMilli())
	if err != nil {
		return nil, mapErr(err)
	}
	if err := expectOne(res); err != nil {
		return nil, storage.ErrLeaseLost
	}
	var r storage.SessionRecord
	var handoff int
	var expiresAt, createdAt, updatedAt int64
	err = tx.QueryRowContext(ctx,
		`SELECT session_id, account, ip, stage, server_name, character_name, handoff_used,
		 expires_at, created_at, updated_at FROM sessions WHERE session_id=?`, sessionID).Scan(
		&r.SessionID, &r.Account, &r.IP, &r.Stage, &r.ServerName, &r.CharacterName,
		&handoff, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapErr(err)
	}
	r.HandoffUsed = handoff != 0
	r.ExpiresAt = time.UnixMilli(expiresAt)
	r.CreatedAt = time.Unix(createdAt, 0)
	r.UpdatedAt = time.Unix(updatedAt, 0)
	return &r, nil
}

func (s *sessionStore) IssueGameTicket(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO game_tickets(session_id, character_name, expires_at, consumed)
		 SELECT ?,?,?,0 WHERE EXISTS (
		   SELECT 1 FROM account_sessions a JOIN sessions s ON s.session_id=a.session_id
		   WHERE a.account=? AND a.session_id=? AND a.expires_at>? AND s.expires_at>?
		     AND s.stage=4 AND s.character_name=?)
		 ON CONFLICT(session_id, character_name) DO UPDATE SET
		 expires_at=excluded.expires_at, consumed=0`,
		sessionID, character, expires.UnixMilli(), account, sessionID,
		now.UnixMilli(), now.UnixMilli(), character)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n != 1 {
		return storage.ErrLeaseLost
	}
	return nil
}

func (s *sessionStore) ClaimGameLease(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE game_tickets SET consumed=1 WHERE session_id=? AND character_name=?
		 AND consumed=0 AND expires_at>? AND EXISTS (
		   SELECT 1 FROM account_sessions a JOIN sessions s ON s.session_id=a.session_id
		   WHERE a.account=? AND a.session_id=? AND a.expires_at>? AND s.expires_at>?
		     AND s.stage=4 AND s.character_name=?)`,
		sessionID, character, now.UnixMilli(), account, sessionID,
		now.UnixMilli(), now.UnixMilli(), character)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, mapErr(err)
	}
	res, err = tx.ExecContext(ctx,
		`INSERT INTO game_leases(account, session_id, character_name, expires_at) VALUES(?,?,?,?)
		 ON CONFLICT(account, character_name) DO UPDATE SET session_id=excluded.session_id,
		 expires_at=excluded.expires_at WHERE game_leases.expires_at<=?`,
		account, sessionID, character, expires.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, mapErr(err)
	}
	n, err = res.RowsAffected()
	if err != nil || n != 1 {
		return false, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return false, mapErr(err)
	}
	return true, nil
}

func (s *sessionStore) RenewGameLease(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE game_leases SET expires_at=? WHERE account=? AND session_id=? AND character_name=?
		 AND expires_at>? AND EXISTS (
		   SELECT 1 FROM account_sessions a JOIN sessions s ON s.session_id=a.session_id
		   WHERE a.account=? AND a.session_id=? AND a.expires_at>? AND s.expires_at>? AND s.stage=4)`,
		expires.UnixMilli(), account, sessionID, character, now.UnixMilli(), account, sessionID,
		now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE account_sessions SET expires_at=? WHERE account=? AND session_id=?`,
		expires.UnixMilli(), account, sessionID); err != nil {
		return false, mapErr(err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET expires_at=?, updated_at=? WHERE session_id=? AND account=?`,
		expires.UnixMilli(), now.Unix(), sessionID, account); err != nil {
		return false, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return false, mapErr(err)
	}
	return true, nil
}

func (s *sessionStore) ReleaseGameLease(ctx context.Context, account string, sessionID int32, character string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM game_leases WHERE account=? AND session_id=? AND character_name=?`,
		account, sessionID, character)
	return mapErr(err)
}

func (s *sessionStore) ReleaseCurrent(ctx context.Context, account string, sessionID int32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_leases WHERE account=? AND session_id=?`, account, sessionID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_tickets WHERE session_id=?`, sessionID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account=? AND session_id=?`, account, sessionID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE session_id=? AND account=?`, sessionID, account); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

func (s *sessionStore) CleanupExpired(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM game_leases WHERE expires_at<=?`,
		`DELETE FROM game_tickets WHERE expires_at<=?`,
		`DELETE FROM account_sessions WHERE expires_at<=?`,
		`DELETE FROM sessions WHERE expires_at<=? AND NOT EXISTS (SELECT 1 FROM game_leases g WHERE g.session_id=sessions.session_id)`,
	} {
		if _, err := tx.ExecContext(ctx, q, now.UnixMilli()); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

func (s *sessionStore) Delete(ctx context.Context, sessionID int32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM game_leases WHERE session_id=?`,
		`DELETE FROM game_tickets WHERE session_id=?`,
		`DELETE FROM account_sessions WHERE session_id=?`,
		`DELETE FROM sessions WHERE session_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, sessionID); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

func (s *sessionStore) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE updated_at<?
		 AND NOT EXISTS (SELECT 1 FROM game_leases g WHERE g.session_id=sessions.session_id)
		 AND NOT EXISTS (SELECT 1 FROM account_sessions a WHERE a.session_id=sessions.session_id)`,
		t.Unix())
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// ---------- 行会 ----------

type guildStore struct{ db *sql.DB }

func (s *guildStore) Create(ctx context.Context, g *storage.Guild) error {
	data, err := marshalGuild(g)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO guilds (name, data, updated_at) VALUES (?,?,?)`,
		g.Name, data, time.Now().Unix())
	return mapErr(err)
}

func (s *guildStore) GetByName(ctx context.Context, name string) (*storage.Guild, error) {
	const q = `SELECT name, data FROM guilds WHERE name = ?`
	var (
		g    storage.Guild
		data []byte
	)
	if err := s.db.QueryRowContext(ctx, q, name).Scan(&g.Name, &data); err != nil {
		return nil, mapErr(err)
	}
	if err := unmarshalGuildInto(&g, data); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *guildStore) List(ctx context.Context) ([]*storage.Guild, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, data FROM guilds ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*storage.Guild
	for rows.Next() {
		var (
			g    storage.Guild
			data []byte
		)
		if err := rows.Scan(&g.Name, &data); err != nil {
			return nil, err
		}
		if err := unmarshalGuildInto(&g, data); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

func (s *guildStore) Save(ctx context.Context, g *storage.Guild) error {
	data, err := marshalGuild(g)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE guilds SET data=?, updated_at=? WHERE name=?`,
		data, time.Now().Unix(), g.Name)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

func (s *guildStore) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM guilds WHERE name=?`, name)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res)
}

// marshalGuild 序列化行会。
//
// 用 JSON 而非 protobuf：行会结构简单（无二进制字段、无定长约束），
// 且只在本进程读写；JSON 可直接在 sqlite3 里肉眼排查，收益大于体积。
func marshalGuild(g *storage.Guild) ([]byte, error) {
	if g == nil {
		return nil, errors.New("sqlite: 行会为 nil")
	}
	b, err := json.Marshal(g)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 序列化行会 %s: %w", g.Name, err)
	}
	return b, nil
}

func unmarshalGuildInto(g *storage.Guild, b []byte) error {
	g.Notice, g.Allies, g.Wars, g.Ranks, g.EnableAuthAlly = nil, nil, nil, nil, false
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, g); err != nil {
		return fmt.Errorf("sqlite: 解析行会 %s: %w", g.Name, err)
	}
	return nil
}

// ---------- 辅助 ----------

func marshalAccount(d *pb.AccountData) ([]byte, error) {
	if d == nil {
		d = &pb.AccountData{}
	}
	return proto.Marshal(d)
}

func unmarshalAccount(b []byte) (*pb.AccountData, error) {
	d := &pb.AccountData{}
	if len(b) == 0 {
		return d, nil
	}
	if err := proto.Unmarshal(b, d); err != nil {
		return nil, fmt.Errorf("sqlite: 解析 AccountData: %w", err)
	}
	return d, nil
}

func marshalCharacter(d *pb.CharacterData) ([]byte, error) {
	if d == nil {
		d = &pb.CharacterData{}
	}
	return proto.Marshal(d)
}

func unmarshalCharacter(b []byte) (*pb.CharacterData, error) {
	d := &pb.CharacterData{}
	if len(b) == 0 {
		return d, nil
	}
	if err := proto.Unmarshal(b, d); err != nil {
		return nil, fmt.Errorf("sqlite: 解析 CharacterData: %w", err)
	}
	return d, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// expectOne 校验恰好影响一行，未命中则报 ErrNotFound。
func expectOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// ---------- 城堡 ----------

type castleStore struct{ db *sql.DB }

func (s *castleStore) Get(ctx context.Context, configDir string) (*storage.Castle, error) {
	const q = `SELECT config_dir, name, data FROM castles WHERE config_dir = ?`
	var (
		c    storage.Castle
		name string
		data []byte
	)
	if err := s.db.QueryRowContext(ctx, q, configDir).Scan(&c.ConfigDir, &name, &data); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("sqlite: 反序列化城堡 %s: %w", configDir, err)
	}
	// 列名是权威（可能被改过），JSON 里那份以防万一。
	c.ConfigDir = configDir
	c.Name = name
	return &c, nil
}

func (s *castleStore) List(ctx context.Context) ([]*storage.Castle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT config_dir, name, data FROM castles ORDER BY config_dir`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*storage.Castle
	for rows.Next() {
		var (
			c    storage.Castle
			name string
			data []byte
		)
		if err := rows.Scan(&c.ConfigDir, &name, &data); err != nil {
			return nil, mapErr(err)
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化城堡 %s: %w", c.ConfigDir, err)
		}
		c.Name = name
		out = append(out, &c)
	}
	return out, rows.Err()
}

// Save 用 upsert 语义写入（原版城堡固定就那一条记录，不区分新建/更新）。
func (s *castleStore) Save(ctx context.Context, c *storage.Castle) error {
	if c == nil {
		return errors.New("sqlite: 城堡为 nil")
	}
	if c.ConfigDir == "" {
		return errors.New("sqlite: 城堡 ConfigDir 为空")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化城堡 %s: %w", c.ConfigDir, err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO castles (config_dir, name, data, updated_at) VALUES (?,?,?,?)
		 ON CONFLICT(config_dir) DO UPDATE SET name=excluded.name, data=excluded.data, updated_at=excluded.updated_at`,
		c.ConfigDir, c.Name, data, time.Now().Unix())
	return mapErr(err)
}
