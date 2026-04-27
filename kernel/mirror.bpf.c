// SPDX-License-Identifier: GPL-2.0
//
// TC ingress eBPF program: captures TCP packets on a target port and emits
// them to userspace via a ring buffer for HTTP mirroring.
//
// Attach point: tc ingress on the network interface facing incoming traffic.

#include <linux/bpf.h>
#include <linux/pkt_cls.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>

// Maximum TCP payload bytes to capture per packet.
// Covers typical HTTP request headers + small bodies.
#define MAX_PAYLOAD_SIZE 1500

// Event sent to userspace for each captured TCP segment.
// Field order is chosen for natural alignment (no implicit padding).
// Go side must parse exactly these offsets:
//   0  src_ip       4 bytes
//   4  dst_ip       4 bytes
//   8  src_port     2 bytes
//  10  dst_port     2 bytes
//  12  payload_len  2 bytes
//  14  tcp_flags    1 byte  (bit0=FIN bit1=SYN bit2=RST bit3=PSH bit4=ACK)
//  15  _pad         1 byte  (explicit, keeps payload 4-byte aligned)
//  16  payload      MAX_PAYLOAD_SIZE bytes
struct pkt_event {
    __u32 src_ip;
    __u32 dst_ip;
    __u16 src_port;
    __u16 dst_port;
    __u16 payload_len;
    __u8  tcp_flags;
    __u8  _pad;
    __u8  payload[MAX_PAYLOAD_SIZE];
};

// Ring buffer: primary channel from kernel to userspace.
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 23); // 8 MB
} rb SEC(".maps");

// Single-entry config: which destination port to mirror.
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u16);
} cfg_target_port SEC(".maps");

// Counters: [0]=total matched packets, [1]=ringbuf drops.
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 2);
    __type(key, __u32);
    __type(value, __u64);
} counters SEC(".maps");

SEC("tc")
int mirror_ingress(struct __sk_buff *skb)
{
    void *data     = (void *)(long)skb->data;
    void *data_end = (void *)(long)skb->data_end;

    /* ── Ethernet ─────────────────────────────────────────── */
    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return TC_ACT_OK;
    if (bpf_ntohs(eth->h_proto) != 0x0800) /* not IPv4 */
        return TC_ACT_OK;

    /* ── IPv4 ─────────────────────────────────────────────── */
    struct iphdr *iph = (void *)(eth + 1);
    if ((void *)(iph + 1) > data_end)
        return TC_ACT_OK;
    if (iph->protocol != 6) /* not TCP */
        return TC_ACT_OK;

    __u32 iph_len = iph->ihl * 4;

    /* ── TCP ──────────────────────────────────────────────── */
    struct tcphdr *tcph = (void *)iph + iph_len;
    if ((void *)(tcph + 1) > data_end)
        return TC_ACT_OK;

    /* Filter by destination port (set by userspace loader) */
    __u32 k = 0;
    __u16 *target = bpf_map_lookup_elem(&cfg_target_port, &k);
    if (!target || bpf_ntohs(tcph->dest) != *target)
        return TC_ACT_OK;

    /* Bump matched-packet counter */
    __u64 *cnt = bpf_map_lookup_elem(&counters, &k);
    if (cnt)
        __sync_fetch_and_add(cnt, 1);

    __u32 tcph_len   = tcph->doff * 4;
    __u32 hdr_offset = sizeof(struct ethhdr) + iph_len + tcph_len;
    __u32 pkt_len    = skb->len;

    /* Skip pure ACKs with no payload */
    if (pkt_len <= hdr_offset)
        return TC_ACT_OK;

    __u32 payload_len = pkt_len - hdr_offset;

    /* Reserve a ring buffer slot */
    struct pkt_event *ev = bpf_ringbuf_reserve(&rb, sizeof(*ev), 0);
    if (!ev) {
        __u32 drop_k = 1;
        __u64 *drops = bpf_map_lookup_elem(&counters, &drop_k);
        if (drops)
            __sync_fetch_and_add(drops, 1);
        return TC_ACT_OK;
    }

    ev->src_ip   = iph->saddr;
    ev->dst_ip   = iph->daddr;
    ev->src_port = bpf_ntohs(tcph->source);
    ev->dst_port = bpf_ntohs(tcph->dest);

    /* Clamp copy length — verifier sees copy_len <= MAX_PAYLOAD_SIZE */
    __u32 copy_len = payload_len < MAX_PAYLOAD_SIZE ? payload_len : MAX_PAYLOAD_SIZE;
    ev->payload_len = (__u16)copy_len;
    ev->tcp_flags = ((__u8)tcph->fin)       |
                    ((__u8)tcph->syn  << 1) |
                    ((__u8)tcph->rst  << 2) |
                    ((__u8)tcph->psh  << 3) |
                    ((__u8)tcph->ack  << 4);
    ev->_pad = 0;

    if (copy_len > 0 && copy_len <= MAX_PAYLOAD_SIZE) {
        long r = bpf_skb_load_bytes(skb, hdr_offset, ev->payload, copy_len);
        if (r < 0)
            ev->payload_len = 0;
    }

    bpf_ringbuf_submit(ev, 0);
    return TC_ACT_OK;
}

char LICENSE[] SEC("license") = "GPL";
