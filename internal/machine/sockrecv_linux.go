//go:build linux

package machine

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A CLI that reads its sockets with recv(2), as Python and Rust do, leaves rchar alone while a
// model streams to it, so ReadBytes also counts what its TCP sockets received (tcp_info's
// bytes_received, through NETLINK_SOCK_DIAG as ss -ti does; no root needed). The bytes arrive
// while the model streams, also while it only thinks, through its keep-alive events.

const (
	inetDiagInfo  = 2  // INET_DIAG_INFO: the tcp_info of each socket
	sizeofDiagReq = 56 // struct inet_diag_req_v2
	sizeofDiagMsg = 72 // struct inet_diag_msg
	diagInodeAt   = 68 // its idiag_inode
	diagCacheFor  = 800 * time.Millisecond
)

var tcpRecvd = struct {
	sync.Mutex
	at   time.Time
	byIn map[uint64]uint64 // socket inode → bytes received
}{}

// socketsReceived adds up the bytes the TCP sockets of pid have received.
func socketsReceived(pids []int) uint64 {
	inodes := map[uint64]bool{}
	for _, p := range pids {
		for _, in := range socketInodes(p) {
			inodes[in] = true
		}
	}
	if len(inodes) == 0 {
		return 0
	}
	byIn := tcpReceived()
	var total uint64
	for in := range inodes {
		total += byIn[in]
	}
	return total
}

// socketInodes lists the inodes of the sockets pid has open.
func socketInodes(pid int) []uint64 {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []uint64
	for _, e := range ents {
		l, err := os.Readlink(dir + "/" + e.Name())
		if err != nil || !strings.HasPrefix(l, "socket:[") {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]"), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// tcpReceived is bytes_received by socket inode for every TCP socket of This PC, one dump shared
// by all the consoles for a moment.
func tcpReceived() map[uint64]uint64 {
	tcpRecvd.Lock()
	defer tcpRecvd.Unlock()
	if time.Since(tcpRecvd.at) < diagCacheFor && tcpRecvd.byIn != nil {
		return tcpRecvd.byIn
	}
	out := map[uint64]uint64{}
	for _, fam := range []uint8{unix.AF_INET, unix.AF_INET6} {
		_ = dumpTCP(fam, out)
	}
	tcpRecvd.at, tcpRecvd.byIn = time.Now(), out
	return out
}

func dumpTCP(family uint8, out map[uint64]uint64) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	req := make([]byte, unix.NLMSG_HDRLEN+sizeofDiagReq)
	ne := binary.NativeEndian
	ne.PutUint32(req[0:], uint32(len(req)))
	ne.PutUint16(req[4:], unix.SOCK_DIAG_BY_FAMILY)
	ne.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	ne.PutUint32(req[8:], 1)
	r := req[unix.NLMSG_HDRLEN:]
	r[0], r[1], r[2] = family, unix.IPPROTO_TCP, 1<<(inetDiagInfo-1)
	ne.PutUint32(r[4:], 0xfff) // every TCP state
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	at := unsafe.Offsetof(unix.TCPInfo{}.Bytes_received)
	buf := make([]byte, 64<<10)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			switch m.Header.Type {
			case unix.NLMSG_DONE:
				return nil
			case unix.NLMSG_ERROR:
				return unix.EINVAL
			}
			d := m.Data
			if len(d) < sizeofDiagMsg {
				continue
			}
			inode := uint64(ne.Uint32(d[diagInodeAt:]))
			for a := d[sizeofDiagMsg:]; len(a) >= 4; {
				l, t := int(ne.Uint16(a[0:])), ne.Uint16(a[2:])
				if l < 4 || l > len(a) {
					break
				}
				if p := a[4:l]; t == inetDiagInfo && uintptr(len(p)) >= at+8 {
					out[inode] = ne.Uint64(p[at:])
				}
				a = a[min(len(a), (l+3)&^3):]
			}
		}
	}
}
