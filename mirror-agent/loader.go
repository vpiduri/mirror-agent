package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// PktEvent mirrors the kernel-side struct pkt_event.
// Must stay byte-for-byte in sync with mirror.bpf.c.
type PktEvent struct {
	SrcIP      uint32
	DstIP      uint32
	SrcPort    uint16
	DstPort    uint16
	TCPFlags   uint8
	PayloadLen uint16
	Payload    [1500]byte
}

// Loader owns the eBPF collection, TC filter, and ring buffer reader.
type Loader struct {
	coll    *ebpf.Collection
	reader  *ringbuf.Reader
	filter  netlink.Filter
	qdisc   netlink.Qdisc
	link    netlink.Link
}

func NewLoader(objPath, iface string, targetPort uint16) (*Loader, error) {
	// Load the compiled eBPF object file.
	spec, err := ebpf.LoadCollectionSpec(objPath)
	if err != nil {
		return nil, fmt.Errorf("load collection spec: %w", err)
	}

	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("new collection: %w", err)
	}

	// Write target port into the cfg_target_port map.
	cfgMap := coll.Maps["cfg_target_port"]
	if cfgMap == nil {
		coll.Close()
		return nil, fmt.Errorf("cfg_target_port map not found in BPF object")
	}
	k := uint32(0)
	if err := cfgMap.Put(k, targetPort); err != nil {
		coll.Close()
		return nil, fmt.Errorf("set target port: %w", err)
	}

	// Open ring buffer reader.
	rbMap := coll.Maps["rb"]
	if rbMap == nil {
		coll.Close()
		return nil, fmt.Errorf("rb map not found in BPF object")
	}
	reader, err := ringbuf.NewReader(rbMap)
	if err != nil {
		coll.Close()
		return nil, fmt.Errorf("ringbuf reader: %w", err)
	}

	// Resolve network interface.
	link, err := netlink.LinkByName(iface)
	if err != nil {
		reader.Close()
		coll.Close()
		return nil, fmt.Errorf("link %q: %w", iface, err)
	}

	// Add clsact qdisc (needed for TC eBPF programs).
	qdisc := &netlink.GenericQdisc{
		QdiscAttrs: netlink.QdiscAttrs{
			LinkIndex: link.Attrs().Index,
			Handle:    netlink.MakeHandle(0xffff, 0),
			Parent:    netlink.HANDLE_CLSACT,
		},
		QdiscType: "clsact",
	}
	// Ignore "already exists" error — idempotent.
	if err := netlink.QdiscAdd(qdisc); err != nil && !os.IsExist(err) {
		// Try to continue — it may already exist from a previous run.
		log.Printf("warning: qdisc add: %v (continuing)", err)
	}

	// Attach the TC eBPF program at ingress.
	prog := coll.Programs["mirror_ingress"]
	if prog == nil {
		reader.Close()
		coll.Close()
		return nil, fmt.Errorf("mirror_ingress program not found in BPF object")
	}

	filter := &netlink.BpfFilter{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: link.Attrs().Index,
			Parent:    netlink.HANDLE_MIN_INGRESS,
			Handle:    netlink.MakeHandle(0, 1),
			Protocol:  unix.ETH_P_ALL,
			Priority:  1,
		},
		Fd:           prog.FD(),
		Name:         "mirror_ingress",
		DirectAction: true,
	}
	if err := netlink.FilterAdd(filter); err != nil {
		reader.Close()
		coll.Close()
		return nil, fmt.Errorf("tc filter add: %w", err)
	}

	log.Printf("TC ingress hook attached on %s (ifindex %d)", iface, link.Attrs().Index)

	return &Loader{
		coll:   coll,
		reader: reader,
		filter: filter,
		qdisc:  qdisc,
		link:   link,
	}, nil
}

// pktEventHeaderSize is the byte offset of the payload array in struct pkt_event.
// Layout (matches kernel/mirror.bpf.c, field order chosen for zero padding):
//   0  src_ip       4 bytes
//   4  dst_ip       4 bytes
//   8  src_port     2 bytes
//  10  dst_port     2 bytes
//  12  payload_len  2 bytes
//  14  tcp_flags    1 byte
//  15  _pad         1 byte
//  16  payload[]    up to 1500 bytes
const pktEventHeaderSize = 16

// ReadEvents blocks and calls fn for each ring buffer event. Safe to run in a goroutine.
func (l *Loader) ReadEvents(fn func(*PktEvent)) {
	for {
		rec, err := l.reader.Read()
		if err != nil {
			// reader closed — normal shutdown
			return
		}

		if len(rec.RawSample) < pktEventHeaderSize {
			log.Printf("short ring buffer record: %d bytes (need at least %d)", len(rec.RawSample), pktEventHeaderSize)
			continue
		}

		ev := &PktEvent{}
		ev.SrcIP = binary.LittleEndian.Uint32(rec.RawSample[0:4])
		ev.DstIP = binary.LittleEndian.Uint32(rec.RawSample[4:8])
		ev.SrcPort = binary.LittleEndian.Uint16(rec.RawSample[8:10])
		ev.DstPort = binary.LittleEndian.Uint16(rec.RawSample[10:12])
		ev.PayloadLen = binary.LittleEndian.Uint16(rec.RawSample[12:14])
		ev.TCPFlags = rec.RawSample[14]
		// byte 15 is _pad — skip

		if ev.PayloadLen > 1500 {
			ev.PayloadLen = 1500
		}
		available := len(rec.RawSample) - pktEventHeaderSize
		copyLen := int(ev.PayloadLen)
		if copyLen > available {
			copyLen = available
		}
		if copyLen > 0 {
			copy(ev.Payload[:copyLen], rec.RawSample[pktEventHeaderSize:pktEventHeaderSize+copyLen])
		}

		fn(ev)
	}
}

// PrintStats logs the eBPF counters (total packets, ring buffer drops).
func (l *Loader) PrintStats() {
	m := l.coll.Maps["counters"]
	if m == nil {
		return
	}
	var v uint64
	k := uint32(0)
	if err := m.Lookup(k, &v); err == nil {
		log.Printf("stats: matched_packets=%d", v)
	}
	k = 1
	if err := m.Lookup(k, &v); err == nil {
		log.Printf("stats: ringbuf_drops=%d", v)
	}
}

func (l *Loader) Close() {
	l.PrintStats()
	l.reader.Close()
	// Remove the TC filter before closing the collection so the FD is still valid.
	if err := netlink.FilterDel(l.filter); err != nil {
		log.Printf("filter del: %v", err)
	}
	l.coll.Close()
}

// ipStr converts a uint32 IP (network byte order) to dotted-decimal.
func ipStr(ip uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, ip)
	return net.IP(b).String()
}
