//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"syscall"
)

const (
	CN_IDX_PROC          = 0x1
	CN_VAL_PROC          = 0x1
	PROC_EVENT_EXEC      = 0x00000002
	PROC_CN_MCAST_LISTEN = 1
	cnMsgHeaderSize      = 20 // fixed header before payload
	procEventMinSize     = 24 // What+CPU+Timestamp+ExecPid+ExecTgid

	netlinkConnector = 11 // NETLINK_CONNECTOR (not in syscall package)
)

// cnMsg is the connector message header (20 bytes).
type cnMsg struct {
	Idx   uint32
	Val   uint32
	Seq   uint32
	Ack   uint32
	Len   uint16
	Flags uint16
}

// procEvent holds the fields relevant to PROC_EVENT_EXEC (24 bytes).
type procEvent struct {
	What      uint32
	CPU       uint32
	Timestamp uint64
	ExecPid   uint32
	ExecTgid  uint32
}

// procOp is the 4-byte payload for the subscribe/unsubscribe message.
type procOp struct{ Op uint32 }

// sendSubscribe sends a 40-byte connector subscribe message:
// NlMsghdr(16) + cnMsg(20) + procOp(4).
func sendSubscribe(fd int, op uint32) error {
	payload := new(bytes.Buffer)
	cn := cnMsg{
		Idx: CN_IDX_PROC,
		Val: CN_VAL_PROC,
		Len: 4, // sizeof(procOp)
	}
	if err := binary.Write(payload, binary.NativeEndian, cn); err != nil {
		return err
	}
	if err := binary.Write(payload, binary.NativeEndian, procOp{Op: op}); err != nil {
		return err
	}

	msg := new(bytes.Buffer)
	hdr := syscall.NlMsghdr{
		Len:  uint32(16 + payload.Len()),
		Type: syscall.NLMSG_DONE,
		Pid:  uint32(syscall.Getpid()),
	}
	if err := binary.Write(msg, binary.NativeEndian, hdr); err != nil {
		return err
	}
	msg.Write(payload.Bytes())

	return syscall.Sendto(fd, msg.Bytes(), 0, &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	})
}

// parseCnProcExec extracts the PID from a PROC_EVENT_EXEC connector message.
// Returns (pid, true) on a matching exec event, (0, false) otherwise.
func parseCnProcExec(data []byte) (int32, bool) {
	if len(data) < cnMsgHeaderSize+procEventMinSize {
		return 0, false
	}
	r := bytes.NewReader(data)

	var cn cnMsg
	if err := binary.Read(r, binary.NativeEndian, &cn); err != nil {
		return 0, false
	}
	if cn.Idx != CN_IDX_PROC || cn.Val != CN_VAL_PROC {
		return 0, false
	}

	var ev procEvent
	if err := binary.Read(r, binary.NativeEndian, &ev); err != nil {
		return 0, false
	}
	if ev.What != PROC_EVENT_EXEC {
		return 0, false
	}
	return int32(ev.ExecPid), true
}

// listenProcExec opens a netlink proc connector socket and returns a channel
// that receives the PID of every exec()d process. Requires CAP_NET_ADMIN;
// returns an error (typically EPERM) if the capability is absent, allowing
// the caller to fall back to polling.
func listenProcExec(ctx context.Context) (<-chan int32, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, netlinkConnector)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}

	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
		Groups: CN_IDX_PROC,
	}); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("netlink bind: %w", err)
	}

	if err := sendSubscribe(fd, PROC_CN_MCAST_LISTEN); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("netlink subscribe: %w", err)
	}

	ch := make(chan int32, 64)
	go func() {
		defer syscall.Close(fd)
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := syscall.Read(fd, buf)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				if err == syscall.ENOBUFS {
					log.Printf("netlink: kernel dropped proc events (ENOBUFS), continuing")
					continue
				}
				log.Printf("netlink read error: %v", err)
				return
			}

			msgs, err := syscall.ParseNetlinkMessage(buf[:n])
			if err != nil {
				continue
			}
			for _, msg := range msgs {
				pid, ok := parseCnProcExec(msg.Data)
				if !ok {
					continue
				}
				select {
				case ch <- pid:
				default:
					log.Printf("netlink: pid channel full, dropping event for pid %d", pid)
				}
			}
		}
	}()

	return ch, nil
}
