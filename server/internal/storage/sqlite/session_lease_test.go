package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

func TestSessionLeaseTakeoverFencesOldGameAndSave(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lease.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	now := time.Now()
	old := &storage.SessionRecord{SessionID: 10001, Account: "account", Stage: 4, CharacterName: "hero", ExpiresAt: now.Add(time.Minute)}
	if err := first.Sessions().Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := first.Sessions().Activate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := first.Sessions().IssueGameTicket(ctx, old.Account, old.SessionID,
		old.CharacterName, now, old.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	claimed, err := first.Sessions().ClaimGameLease(ctx, old.Account, old.SessionID, old.CharacterName,
		time.Now(), time.Now().Add(8*time.Second))
	if err != nil || !claimed {
		t.Fatalf("old game lease: claimed=%v err=%v", claimed, err)
	}
	if renewed, err := first.Sessions().RenewGameLease(ctx, old.Account, old.SessionID, old.CharacterName,
		time.Now(), time.Now().Add(8*time.Second)); err != nil || !renewed {
		t.Fatalf("old game lease renewal: renewed=%v err=%v", renewed, err)
	}

	chr := &storage.Character{Account: old.Account, Data: &pb.CharacterData{
		ChrName: old.CharacterName, Account: old.Account, Abil: &pb.Ability{Level: 1},
	}}
	chr.SyncFromData()
	if err := first.Characters().Create(ctx, chr); err != nil {
		t.Fatal(err)
	}

	newSession := &storage.SessionRecord{SessionID: 10002, Account: "account", Stage: 4,
		CharacterName: "hero", ExpiresAt: time.Now().Add(time.Minute)}
	if err := second.Sessions().Create(ctx, newSession); err != nil {
		t.Fatal(err)
	}
	if err := second.Sessions().Activate(ctx, newSession); err != nil {
		t.Fatal(err)
	}
	if err := second.Sessions().IssueGameTicket(ctx, newSession.Account, newSession.SessionID,
		newSession.CharacterName, time.Now(), newSession.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if current, err := first.Sessions().IsCurrent(ctx, old.Account, old.SessionID, time.Now()); err != nil || current {
		t.Fatalf("old session current=%v err=%v", current, err)
	}
	if renewed, err := first.Sessions().RenewGameLease(ctx, old.Account, old.SessionID, old.CharacterName,
		time.Now(), time.Now().Add(8*time.Second)); err != nil || renewed {
		t.Fatalf("old lease renewed=%v err=%v", renewed, err)
	}

	// The old owner may flush its final snapshot while its game lease remains held.
	chr.Data.Gold = 77
	if err := first.Characters().UpdateForSession(ctx, chr, old.SessionID); err != nil {
		t.Fatalf("final old-owner save: %v", err)
	}
	if err := first.Sessions().ReleaseGameLease(ctx, old.Account, old.SessionID, old.CharacterName); err != nil {
		t.Fatal(err)
	}
	if current, err := second.Sessions().IsCurrent(ctx, newSession.Account, newSession.SessionID, time.Now()); err != nil || !current {
		t.Fatalf("releasing old session disturbed new owner: current=%v err=%v", current, err)
	}

	claimed, err = second.Sessions().ClaimGameLease(ctx, newSession.Account, newSession.SessionID,
		newSession.CharacterName, time.Now(), time.Now().Add(8*time.Second))
	if err != nil || !claimed {
		t.Fatalf("new game lease after old release: claimed=%v err=%v", claimed, err)
	}
	if err := first.Characters().UpdateForSession(ctx, chr, old.SessionID); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("stale save error=%v, want ErrLeaseLost", err)
	}
	if again, err := first.Sessions().ClaimGameLease(ctx, newSession.Account, newSession.SessionID,
		newSession.CharacterName, time.Now(), time.Now().Add(8*time.Second)); err != nil || again {
		t.Fatalf("replayed game ticket claimed=%v err=%v", again, err)
	}
}

func TestSessionGameLeaseClaimIsAtomicAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "race.db")
	stores := make([]*Store, 2)
	for i := range stores {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = st
		defer st.Close()
	}
	now := time.Now()
	r := &storage.SessionRecord{SessionID: 20001, Account: "racer", Stage: 4,
		CharacterName: "hero", ExpiresAt: now.Add(time.Minute)}
	if err := stores[0].Sessions().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].Sessions().Activate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].Sessions().IssueGameTicket(ctx, r.Account, r.SessionID,
		r.CharacterName, now, r.ExpiresAt); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	errs := make(chan error, 2)
	for _, st := range stores {
		wg.Add(1)
		go func(st *Store) {
			defer wg.Done()
			ok, err := st.Sessions().ClaimGameLease(ctx, r.Account, r.SessionID, r.CharacterName,
				time.Now(), time.Now().Add(8*time.Second))
			if err != nil {
				errs <- err
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(st)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("claim error: %v", err)
	}
	if wins != 1 {
		t.Fatalf("successful game lease claims=%d, want 1", wins)
	}
}

func TestOneAccountSessionMayLeaseDifferentCharacters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	r := &storage.SessionRecord{SessionID: 25001, Account: "peer-account", Stage: 4,
		CharacterName: "main", ExpiresAt: now.Add(time.Minute)}
	if err := s.Sessions().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().Activate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().IssueGameTicket(ctx, r.Account, r.SessionID, "main", now, r.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	mainClaimed, err := s.Sessions().ClaimGameLease(ctx, r.Account, r.SessionID, "main", now, now.Add(8*time.Second))
	if err != nil || !mainClaimed {
		t.Fatalf("main lease=%v err=%v", mainClaimed, err)
	}

	r.CharacterName = "peer"
	r.ExpiresAt = time.Now().Add(time.Minute)
	if err := s.Sessions().Update(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().IssueGameTicket(ctx, r.Account, r.SessionID, "peer", time.Now(), r.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	peerClaimed, err := s.Sessions().ClaimGameLease(ctx, r.Account, r.SessionID, "peer", time.Now(), time.Now().Add(8*time.Second))
	if err != nil || !peerClaimed {
		t.Fatalf("peer lease=%v err=%v", peerClaimed, err)
	}
	for _, name := range []string{"main", "peer"} {
		if ok, err := s.Sessions().RenewGameLease(ctx, r.Account, r.SessionID, name,
			time.Now(), time.Now().Add(8*time.Second)); err != nil || !ok {
			t.Fatalf("renew %s: ok=%v err=%v", name, ok, err)
		}
	}
	if duplicate, err := s.Sessions().ClaimGameLease(ctx, r.Account, r.SessionID, "main", time.Now(), time.Now().Add(8*time.Second)); err != nil || duplicate {
		t.Fatalf("same-character replay claimed=%v err=%v", duplicate, err)
	}
}

func TestSessionHandoffIsOneTime(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	r := &storage.SessionRecord{SessionID: 30001, Account: "handoff", Stage: 2, ExpiresAt: now.Add(time.Minute)}
	if err := s.Sessions().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().Activate(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sessions().BindHandoff(ctx, r.Account, r.SessionID, time.Now()); err != nil {
		t.Fatalf("first handoff: %v", err)
	}
	if _, err := s.Sessions().BindHandoff(ctx, r.Account, r.SessionID, time.Now()); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("replayed handoff=%v, want ErrLeaseLost", err)
	}
}
