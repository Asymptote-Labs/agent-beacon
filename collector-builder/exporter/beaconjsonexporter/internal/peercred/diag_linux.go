//go:build linux

package peercred

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The inet_diag request and reply, as in linux/inet_diag.h. Ports and addresses are in network byte
// order; everything else is in host order.
type inetDiagSockID struct {
	SPort  [2]byte
	DPort  [2]byte
	Src    [16]byte
	Dst    [16]byte
	If     uint32
	Cookie [2]uint32
}

type inetDiagReqV2 struct {
	Family   uint8
	Protocol uint8
	Ext      uint8
	Pad      uint8
	States   uint32
	ID       inetDiagSockID
}

type inetDiagMsg struct {
	Family  uint8
	State   uint8
	Timer   uint8
	Retrans uint8
	ID      inetDiagSockID
	Expires uint32
	RQueue  uint32
	WQueue  uint32
	UID     uint32
	Inode   uint32
}

const (
	sockDiagByFamily = 20         // SOCK_DIAG_BY_FAMILY
	inetDiagNoCookie = 0xFFFFFFFF // INET_DIAG_NOCOOKIE
	diagTimeout      = time.Second
)

// errDiagUnavailable is any failure of the netlink channel itself, as opposed to an answer; the
// caller falls back to /proc on it.
var errDiagUnavailable = errors.New("sock_diag is not available")

// sockDiagUID asks the kernel, over NETLINK_SOCK_DIAG, for the socket whose local end is client and
// remote end is server: one hash lookup (inet_diag_find_one_icsk), not a dump. An IPv4 pair is tried
// as AF_INET and then, for a dual-stack socket, as its IPv4-mapped AF_INET6 form.
func sockDiagUID(client, server *net.TCPAddr) (string, error) {
	type attempt struct {
		family   uint8
		src, dst net.IP
	}
	var attempts []attempt
	if c4, s4 := client.IP.To4(), server.IP.To4(); c4 != nil && s4 != nil {
		attempts = append(attempts, attempt{unix.AF_INET, c4, s4}, attempt{unix.AF_INET6, mapped(c4), mapped(s4)})
	} else {
		attempts = append(attempts, attempt{unix.AF_INET6, client.IP.To16(), server.IP.To16()})
	}
	for _, a := range attempts {
		uid, err := sockDiagOne(a.family, a.src, client.Port, a.dst, server.Port)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return uid, err
	}
	return "", ErrNotFound
}

func sockDiagOne(family uint8, src net.IP, sport int, dst net.IP, dport int) (string, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errDiagUnavailable, err)
	}
	defer unix.Close(fd)
	tv := unix.NsecToTimeval(diagTimeout.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)

	req := inetDiagReqV2{Family: family, Protocol: unix.IPPROTO_TCP, States: 0xFFFFFFFF}
	binary.BigEndian.PutUint16(req.ID.SPort[:], uint16(sport))
	binary.BigEndian.PutUint16(req.ID.DPort[:], uint16(dport))
	if family == unix.AF_INET {
		copy(req.ID.Src[:4], src.To4())
		copy(req.ID.Dst[:4], dst.To4())
	} else {
		copy(req.ID.Src[:], src.To16())
		copy(req.ID.Dst[:], dst.To16())
	}
	req.ID.Cookie = [2]uint32{inetDiagNoCookie, inetDiagNoCookie}

	body := (*[unsafe.Sizeof(req)]byte)(unsafe.Pointer(&req))[:]
	hdr := unix.NlMsghdr{
		Len:   uint32(unix.NLMSG_HDRLEN + len(body)),
		Type:  sockDiagByFamily,
		Flags: unix.NLM_F_REQUEST,
		Seq:   1,
	}
	msg := make([]byte, 0, hdr.Len)
	msg = append(msg, (*[unix.SizeofNlMsghdr]byte)(unsafe.Pointer(&hdr))[:]...)
	msg = append(msg, body...)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return "", fmt.Errorf("%w: %v", errDiagUnavailable, err)
	}

	buf := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errDiagUnavailable, err)
	}
	replies, err := syscall.ParseNetlinkMessage(buf[:n])
	if err != nil {
		return "", fmt.Errorf("%w: %v", errDiagUnavailable, err)
	}
	for _, reply := range replies {
		switch reply.Header.Type {
		case unix.NLMSG_ERROR:
			if len(reply.Data) < 4 {
				return "", errDiagUnavailable
			}
			errno := -int32(binary.NativeEndian.Uint32(reply.Data[:4]))
			if errno == 0 {
				continue
			}
			if syscall.Errno(errno) == syscall.ENOENT {
				// No such socket, or no TCP diag handler to ask: the kernel says ENOENT for both.
				// lookupUID tells them apart.
				return "", ErrNotFound
			}
			return "", fmt.Errorf("%w: %v", errDiagUnavailable, syscall.Errno(errno))
		case sockDiagByFamily:
			var m inetDiagMsg
			if len(reply.Data) < int(unsafe.Sizeof(m)) {
				return "", errDiagUnavailable
			}
			m = *(*inetDiagMsg)(unsafe.Pointer(&reply.Data[0]))
			// A reply with a socket in it, whichever: the TCP diag handler is there, so its ENOENT
			// means no such socket from now on (see lookupUID).
			diag.CompareAndSwap(diagUnknown, diagSeesSockets)
			// The kernel answered for exactly this connection, or the answer is not about it.
			if m.ID.SPort != req.ID.SPort || m.ID.DPort != req.ID.DPort {
				return "", ErrNotFound
			}
			if m.Inode == 0 {
				// TIME_WAIT or orphaned: uid reads 0 whoever created it.
				return "", ErrNotFound
			}
			return strconv.FormatUint(uint64(m.UID), 10), nil
		}
	}
	return "", ErrNotFound
}
