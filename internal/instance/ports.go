package instance

import (
	"errors"
	"net"
	"strconv"

	"github.com/azamrizki/nextboard/internal/config"
)

// Every instance gets a block of 100 ports: block k holds its hub at 12312+100k and its
// previews at 12320+100k … 12339+100k. Block 0 is the default instance's, as it always was.
const (
	BlockSize    = 100
	MaxBlock     = 50
	PreviewPorts = 20

	defaultPreviewStart = 12320
)

// Block is one instance's ports.
type Block struct {
	K            int    `json:"k"`
	HubPort      int    `json:"hubPort"`
	PreviewRange [2]int `json:"previewRange"`
}

// Listen is the block's hub address on loopback, what a new instance listens on by default.
func (b Block) Listen() string { return "127.0.0.1:" + strconv.Itoa(b.HubPort) }

// BlockAt is block k: k = 0 is the default instance's (12312, 12320–12339).
func BlockAt(k int) Block { return blockAt(0, k) }

// blockAt is block k with every port moved by shift (tests use ports of their own).
func blockAt(shift, k int) Block {
	hub := config.DefaultPort + shift + BlockSize*k
	start := defaultPreviewStart + shift + BlockSize*k
	return Block{K: k, HubPort: hub, PreviewRange: [2]int{start, start + PreviewPorts - 1}}
}

// ErrNoFreeBlock: every block up to MaxBlock is taken.
var ErrNoFreeBlock = errors.New("no free block of ports for a new instance: every block up to 12312+5000 is in use or claimed by another instance; pass --listen")

// PickBlock picks a new instance's ports: the smallest block k ≥ 1 (at most MaxBlock) whose hub
// port and every preview port can be bound now and are not claimed by any of siblings (their
// hub, status and preview ports; pass List's result). hubPort > 0 is a port the user chose
// (--listen): it is used as given, and only the preview range is picked, one that does not
// hold hubPort.
func PickBlock(siblings []Instance, hubPort int) (Block, error) {
	return pickBlock(0, siblings, hubPort, PortFree)
}

func pickBlock(shift int, siblings []Instance, hubPort int, free func(int) bool) (Block, error) {
	claimed := map[int]bool{}
	for _, s := range siblings {
		for _, p := range s.Ports() {
			claimed[p] = true
		}
	}
	usable := func(p int) bool { return !claimed[p] && free(p) }
next:
	for k := 1; k <= MaxBlock; k++ {
		b := blockAt(shift, k)
		if hubPort > 0 {
			if hubPort >= b.PreviewRange[0] && hubPort <= b.PreviewRange[1] {
				continue
			}
			b.HubPort = hubPort
		} else if !usable(b.HubPort) {
			continue
		}
		for p := b.PreviewRange[0]; p <= b.PreviewRange[1]; p++ {
			if !usable(p) {
				continue next
			}
		}
		return b, nil
	}
	return Block{}, ErrNoFreeBlock
}

// PortFree reports whether a TCP port can be bound now, on every address and on loopback
// (BSD lets a wildcard bind share a port that one address holds, so both are tried).
func PortFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	for _, addr := range []string{":", "127.0.0.1:"} {
		ln, err := net.Listen("tcp", addr+strconv.Itoa(port))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}
