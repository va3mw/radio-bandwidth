package main

// MTU test: find the largest UDP packet that crosses the path without being
// split (Don't Fragment set), and check whether packets that must be split
// still arrive. VPNs, PPPoE and some firewalls shrink or break this, and the
// result is that small packets work while big ones silently vanish.

import (
	"context"
	"encoding/binary"
	"net"
	"time"
)

type MTUResult struct {
	PathMTU     int  `json:"pathMtu"`     // largest IP packet that passed unsplit
	DFSupported bool `json:"dfSupported"` // could we set Don't Fragment?
	FragTested  bool `json:"fragTested"`
	FragmentsOK bool `json:"fragmentsOk"` // a 3000 byte UDP packet (split) arrived
}

func (a *App) mtuTest(ctx context.Context, host string, port int) *MTUResult {
	res := &MTUResult{}
	ipHdr := 28 // IPv4 + UDP
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		ipHdr = 48
	}

	uc, err := a.dialUDP(host, port)
	if err != nil {
		return res
	}
	defer uc.Close()
	if setDontFragment(uc) == nil {
		res.DFSupported = true
		var seq uint32
		probe := func(payload int) bool {
			for try := 0; try < 2 && ctx.Err() == nil; try++ {
				seq++
				if echoProbe(uc, payload, seq) {
					return true
				}
			}
			return false
		}
		switch {
		case probe(1500 - ipHdr):
			res.PathMTU = 1500
		case !probe(576 - ipHdr):
			res.PathMTU = 0 // even tiny packets fail: no answer
		default:
			lo, hi := 576-ipHdr, 1500-ipHdr // lo passes, hi fails
			for hi-lo > 1 && ctx.Err() == nil {
				mid := (lo + hi) / 2
				if probe(mid) {
					lo = mid
				} else {
					hi = mid
				}
			}
			res.PathMTU = lo + ipHdr
		}
	}

	// Fragmentation: a plain socket, 3000 byte payload, must be split.
	if fc, err := a.dialUDP(host, port); err == nil {
		res.FragTested = true
		for try := uint32(0); try < 3 && !res.FragmentsOK && ctx.Err() == nil; try++ {
			res.FragmentsOK = echoProbe(fc, 3000, 1000+try)
		}
		fc.Close()
	}
	return res
}

// echoProbe sends one RBWF packet of the given payload size and waits for the
// same packet to come back.
func echoProbe(uc *net.UDPConn, size int, seq uint32) bool {
	pkt := make([]byte, size)
	copy(pkt, "RBWF")
	binary.BigEndian.PutUint32(pkt[4:8], 0xFFFF0000|seq) // never matches latency probes
	if _, err := uc.Write(pkt); err != nil {
		return false // EMSGSIZE: bigger than this computer's own MTU
	}
	buf := make([]byte, 65536)
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		_ = uc.SetReadDeadline(deadline)
		n, err := uc.Read(buf)
		if err != nil {
			return false
		}
		if n == size && binary.BigEndian.Uint32(buf[4:8]) == 0xFFFF0000|seq {
			return true
		}
	}
}
