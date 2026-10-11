package gamesvr

import (
	"testing"

	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

func TestTakeBagItemRepro(t *testing.T) {
	d := &pb.CharacterData{
		BagItems: []*pb.UserItem{
			{Index: 1, MakeIndex: 11},
			{Index: 2, MakeIndex: 22},
			{Index: 3, MakeIndex: 33},
			{Index: 4, MakeIndex: 44},
			{},
			{},
		},
	}
	takeBagItemLocked(d, 1) // 卖掉第 1 格（22 号）
	for i, it := range d.BagItems {
		idx, mk := uint32(0), uint32(0)
		if it != nil {
			idx, mk = it.Index, uint32(it.MakeIndex)
		}
		t.Logf("格 %d: Index=%d MakeIndex=%d", i, idx, mk)
	}
}
