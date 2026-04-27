package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	iface := flag.String("iface", "eth0", "network interface to attach TC hook")
	port := flag.Uint("port", 9090, "target port to mirror (APIGEE-sim port)")
	ewpAddr := flag.String("ewp", "http://ewp-sim:9091", "EWP base URL to mirror traffic to")
	bpfObj := flag.String("bpf", "/ebpf/mirror.bpf.o", "path to compiled eBPF object file")
	flag.Parse()

	log.Printf("mirror-agent starting: iface=%s port=%d ewp=%s", *iface, *port, *ewpAddr)

	loader, err := NewLoader(*bpfObj, *iface, uint16(*port))
	if err != nil {
		log.Fatalf("failed to load eBPF program: %v", err)
	}
	defer loader.Close()

	tracker := NewTCPTracker()
	mirror := NewHTTPMirror(*ewpAddr)

	log.Printf("eBPF attached — listening for HTTP on port %d, mirroring to %s", *port, *ewpAddr)

	go startStatsPrinter(30 * time.Second)

	go loader.ReadEvents(func(ev *PktEvent) {
		reqs := tracker.Feed(ev)
		for _, req := range reqs {
			go mirror.Send(req)
		}
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")
	printStats() // final summary on exit
}
