package gamesvr

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
)

func TestSessionLeaseLoopClosesReplacedGameConnection(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now()
	old := &storage.SessionRecord{SessionID: 101, Account: "account", Stage: 4,
		CharacterName: "hero", ExpiresAt: now.Add(time.Minute)}
	if err := store.Sessions().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions().Activate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions().IssueGameTicket(ctx, old.Account, old.SessionID,
		old.CharacterName, now, old.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.Sessions().ClaimGameLease(ctx, old.Account, old.SessionID,
		old.CharacterName, now, now.Add(gameLeaseDuration)); err != nil || !claimed {
		t.Fatalf("old lease claimed=%v err=%v", claimed, err)
	}

	newOwner := &storage.SessionRecord{SessionID: 202, Account: old.Account, Stage: 4,
		CharacterName: old.CharacterName, ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.Sessions().Create(ctx, newOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.Sessions().Activate(ctx, newOwner); err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	p := newTestPlayer(77, old.CharacterName, 0)
	p.Char.Account = old.Account
	p.sessionID = old.SessionID
	p.conn = serverConn

	s := &Server{store: store, world: worldState{players: map[uint32]*Player{p.Obj.ID: p}}}
	s.tickSessionLeases(time.Now())
	if !p.revoked.Load() {
		t.Fatal("old connection was not revoked after account takeover")
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("old socket remained open")
	}
}
