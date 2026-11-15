package instance

import (
	"errors"
	"net"
	"strconv"
	"testing"
)

func TestBlocks(t *testing.T) {
	if b := BlockAt(0); b.HubPort != 12312 || b.PreviewRange != [2]int{12320, 12339} || b.Listen() != "127.0.0.1:12312" {
		t.Errorf("block 0 %+v", b)
	}
	if b := BlockAt(1); b.HubPort != 12412 || b.PreviewRange != [2]int{12420, 12439} || b.K != 1 {
		t.Errorf("block 1 %+v", b)
	}
	if b := BlockAt(MaxBlock); b.HubPort != 17312 || b.PreviewRange[1] != 17339 {
		t.Errorf("last block %+v", b)
	}
	// a block's ports stay inside its hundred
	for k := 0; k <= MaxBlock; k++ {
		b := BlockAt(k)
		lo, hi := 12300+100*k, 12399+100*k
		if b.HubPort < lo || b.PreviewRange[0] < lo || b.PreviewRange[1] > hi || b.HubPort >= b.PreviewRange[0] {
			t.Fatalf("block %d %+v", k, b)
		}
	}
}

func TestPickBlock(t *testing.T) {
	allFree := func(int) bool { return true }
	def := Instance{Name: "default", Listen: "127.0.0.1:12312", PreviewRange: [2]int{12320, 12339}}
	work := Instance{Name: "work", Listen: "127.0.0.1:12412", PreviewRange: [2]int{12420, 12439}}

	if b, err := pickBlock(0, nil, 0, allFree); err != nil || b.K != 1 || b.HubPort != 12412 || b.PreviewRange != [2]int{12420, 12439} {
		t.Errorf("first named instance %+v %v", b, err)
	}
	if b, _ := pickBlock(0, []Instance{def, work}, 0, allFree); b.K != 2 || b.HubPort != 12512 {
		t.Errorf("after work %+v", b)
	}
	// a busy hub port, a busy preview port
	busy := func(ports ...int) func(int) bool {
		return func(p int) bool {
			for _, b := range ports {
				if p == b {
					return false
				}
			}
			return true
		}
	}
	if b, _ := pickBlock(0, []Instance{def, work}, 0, busy(12512)); b.K != 3 {
		t.Errorf("busy hub port %+v", b)
	}
	if b, _ := pickBlock(0, []Instance{def, work}, 0, busy(12537)); b.K != 3 {
		t.Errorf("busy preview port %+v", b)
	}
	// a sibling whose ports are not in a block of their own claim them anyway
	odd := Instance{Name: "odd", Listen: "127.0.0.1:12999", StatusListen: "0.0.0.0:12412", PreviewRange: [2]int{12530, 12625}}
	if b, _ := pickBlock(0, []Instance{def, odd}, 0, allFree); b.K != 4 {
		t.Errorf("claimed ports %+v", b) // 1: 12412 is odd's status; 2: previews; 3: hub; 4 is free
	}
	// --listen given: only previews are picked, around it
	if b, _ := pickBlock(0, []Instance{def}, 12345, allFree); b.K != 1 || b.HubPort != 12345 || b.PreviewRange != [2]int{12420, 12439} {
		t.Errorf("explicit port %+v", b)
	}
	if b, _ := pickBlock(0, []Instance{def}, 12425, allFree); b.K != 2 || b.HubPort != 12425 {
		t.Errorf("explicit port inside block 1's previews %+v", b)
	}
	if b, _ := pickBlock(0, []Instance{def}, 12412, busy(12412)); b.K != 1 || b.HubPort != 12412 {
		t.Errorf("an explicit port is not checked here %+v", b)
	}
	if _, err := pickBlock(0, nil, 0, func(int) bool { return false }); !errors.Is(err, ErrNoFreeBlock) {
		t.Errorf("all busy: %v", err)
	}
}

// With real sockets: a port a listener holds is not free, and its block is skipped. The
// blocks are moved to ports nothing on this machine uses at the moment.
func TestPickBlockSkipsAHeldPort(t *testing.T) {
	shift := -1
	for s := 8000; s < 18000 && shift < 0; s += 700 {
		ok := true
		for k := 1; k <= 2 && ok; k++ {
			b := blockAt(s, k)
			ok = PortFree(b.HubPort)
			for p := b.PreviewRange[0]; p <= b.PreviewRange[1] && ok; p++ {
				ok = PortFree(p)
			}
		}
		if ok {
			shift = s
		}
	}
	if shift < 0 {
		t.Skip("no two free blocks of ports on this machine")
	}
	held := blockAt(shift, 1).HubPort
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(held))
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	if PortFree(held) {
		t.Fatalf("port %d is held but looks free", held)
	}
	b, err := pickBlock(shift, nil, 0, PortFree)
	if err != nil || b.K != 2 || b.HubPort != blockAt(shift, 2).HubPort {
		t.Fatalf("%+v %v", b, err)
	}
	ln.Close()
	if !PortFree(held) {
		t.Fatalf("port %d is free again", held)
	}
	if PortFree(0) || PortFree(70000) {
		t.Fatal("not a port")
	}
}

func TestInstancePorts(t *testing.T) {
	in := Instance{Listen: "127.0.0.1:12412", StatusListen: ":12412", PreviewRange: [2]int{12420, 12422}}
	got := in.Ports()
	if len(got) != 4 || got[0] != 12412 || got[1] != 12420 || got[3] != 12422 {
		t.Fatalf("%v", got)
	}
	if p := (Instance{Listen: "nonsense"}).Ports(); len(p) != 0 {
		t.Fatalf("%v", p)
	}
}
