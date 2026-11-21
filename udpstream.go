package main

// UDP stream test: a steady, fixed-rate UDP stream in each direction, like
// the audio, panadapter and IQ streams ham radio and other real-time apps
// use. Unlike TCP, nothing is resent, so every lost packet is a dropout.
//
// Packets (all on the main test port):
//   RBWS session(8) seq(4) sendNs(8) pad...   stream data, 1200 bytes
//   RBWE session(8) sent(4)                   end of stream, sent 5 times
//   RBWQ session(8) kbps(4) durMs(4)          "send me a stream" (download)
// Upload results are fetched over TCP with op "udpstats".

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

const (
	udpPayload   = 1200 // fits inside a 1280 byte VPN MTU (Tailscale, WireGuard)
	udpStreamDur = 8 * time.Second
	udpMaxKbps   = 400000
	udpMaxDur    = 15 * time.Second
)

// UDPStats is what the receiving end saw.
type UDPStats struct {
	Sent       int     `json:"sent"`
	Received   int     `json:"received"`
	LossPct    float64 `json:"lossPct"`
	JitterMs   float64 `json:"jitterMs"` // one-way, RFC 3550 style
	OutOfOrder int     `json:"outOfOrder"`
	Mbps       float64 `json:"mbps"` // delivered rate
}

// ---------------------------------------------------------------- receiver

type streamRx struct {
	mu        sync.Mutex
	ref       time.Time
	first     time.Time
	last      time.Time
	created   time.Time
	uniq, ooo int
	bytes     int64
	maxSeq    int64
	seen      []uint64
	jitter    float64
	prev      float64
	havePrev  bool
	endSent   int // from the sender's end packets, -1 until known
	endAt     time.Time
}

func newStreamRx() *streamRx {
	now := time.Now()
	return &streamRx{ref: now, created: now, maxSeq: -1, endSent: -1}
}

func (rx *streamRx) add(seq uint32, sendNs int64, n int, now time.Time) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	s := int64(seq)
	if s > 4_000_000 {
		return
	}
	w := int(s / 64)
	for len(rx.seen) <= w {
		rx.seen = append(rx.seen, 0)
	}
	bit := uint64(1) << uint(s%64)
	if rx.seen[w]&bit != 0 {
		return // duplicate
	}
	rx.seen[w] |= bit
	if rx.first.IsZero() {
		rx.first = now
	}
	rx.last = now
	rx.uniq++
	rx.bytes += int64(n)
	if s < rx.maxSeq {
		rx.ooo++
	} else {
		rx.maxSeq = s
	}
	// Relative transit time; the unknown clock offset cancels out.
	transit := float64(now.Sub(rx.ref).Nanoseconds()-sendNs) / 1e6
	if rx.havePrev {
		rx.jitter += (math.Abs(transit-rx.prev) - rx.jitter) / 16
	}
	rx.prev, rx.havePrev = transit, true
}

func (rx *streamRx) end(sent int) {
	rx.mu.Lock()
	if rx.endSent < 0 {
		rx.endSent, rx.endAt = sent, time.Now()
	}
	rx.mu.Unlock()
}

// stats; sent < 0 means "use what the sender's end packet said".
func (rx *streamRx) stats(sent int) UDPStats {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	if sent < 0 {
		sent = rx.endSent
	}
	if sent < 0 {
		sent = int(rx.maxSeq + 1)
	}
	st := UDPStats{Sent: sent, Received: rx.uniq, JitterMs: rx.jitter, OutOfOrder: rx.ooo}
	if sent > 0 {
		lost := sent - rx.uniq
		if lost < 0 {
			lost = 0
		}
		st.LossPct = float64(lost) * 100 / float64(sent)
	}
	if d := rx.last.Sub(rx.first).Seconds(); d > 0.2 {
		st.Mbps = float64(rx.bytes) * 8 / d / 1e6
	}
	return st
}

// ---------------------------------------------------------------- sender

func streamPackets(rateMbps float64) float64 {
	return rateMbps * 1e6 / (udpPayload * 8)
}

// sendStream paces fixed size packets at rateMbps for dur and returns how many
// went out. Packets are sent in small catch-up bursts so it keeps the rate
// even where the OS timer is coarse.
func sendStream(ctx context.Context, write func([]byte) error, session []byte, rateMbps float64, dur time.Duration, onSent func(int)) int {
	pkt := make([]byte, udpPayload)
	_, _ = rand.Read(pkt)
	copy(pkt, "RBWS")
	copy(pkt[4:12], session)
	pps := streamPackets(rateMbps)
	start := time.Now()
	sent := 0
	for ctx.Err() == nil {
		el := time.Since(start)
		if el >= dur {
			break
		}
		due := int(el.Seconds()*pps) + 1
		for sent < due {
			binary.BigEndian.PutUint32(pkt[12:16], uint32(sent))
			binary.BigEndian.PutUint64(pkt[16:24], uint64(time.Since(start).Nanoseconds()))
			if write(pkt) != nil {
				// Local buffer full (common on Windows at high rates): the
				// packet was not sent, so do not count it; try again shortly.
				break
			}
			sent++
		}
		if onSent != nil {
			onSent(sent)
		}
		time.Sleep(time.Millisecond)
	}
	end := make([]byte, 16)
	copy(end, "RBWE")
	copy(end[4:12], session)
	binary.BigEndian.PutUint32(end[12:16], uint32(sent))
	for i := 0; i < 5; i++ {
		_ = write(end)
		time.Sleep(20 * time.Millisecond)
	}
	return sent
}

// ---------------------------------------------------------------- server side

type udpServer struct {
	mu      sync.Mutex
	rx      map[string]*streamRx
	tx      map[string]bool
	allowed map[string]time.Time // IPs that recently spoke to us over TCP
}

func (u *udpServer) allow(ip string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.allowed == nil {
		u.allowed = map[string]time.Time{}
	}
	u.allowed[ip] = time.Now()
}

// mayStreamTo stops Radio Bandwidth being used to flood a third party with spoofed
// requests: only addresses that just completed a TCP handshake qualify.
func (u *udpServer) mayStreamTo(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	t, ok := u.allowed[ip.String()]
	return ok && time.Since(t) < 2*time.Minute
}

func (u *udpServer) rxFor(session string, create bool) *streamRx {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rx == nil {
		u.rx = map[string]*streamRx{}
	}
	for k, r := range u.rx { // forget abandoned sessions
		if time.Since(r.created) > 2*time.Minute {
			delete(u.rx, k)
		}
	}
	rx := u.rx[session]
	if rx == nil && create && len(u.rx) < 16 {
		rx = newStreamRx()
		u.rx[session] = rx
	}
	return rx
}

func (u *udpServer) take(session string) *streamRx {
	u.mu.Lock()
	defer u.mu.Unlock()
	rx := u.rx[session]
	delete(u.rx, session)
	return rx
}

// handleStreamPacket is called from the UDP read loop for RBWS/RBWE/RBWQ.
func (a *App) handleStreamPacket(uc *net.UDPConn, p []byte, from *net.UDPAddr, now time.Time) {
	if len(p) < 12 {
		return
	}
	session := hex.EncodeToString(p[4:12])
	switch string(p[:4]) {
	case "RBWS":
		if len(p) < 24 {
			return
		}
		if rx := a.us.rxFor(session, true); rx != nil {
			rx.add(binary.BigEndian.Uint32(p[12:16]), int64(binary.BigEndian.Uint64(p[16:24])), len(p), now)
		}
	case "RBWE":
		if len(p) >= 16 {
			if rx := a.us.rxFor(session, false); rx != nil {
				rx.end(int(binary.BigEndian.Uint32(p[12:16])))
			}
		}
	case "RBWQ":
		if len(p) < 20 || !a.us.mayStreamTo(from.IP) {
			return
		}
		kbps := binary.BigEndian.Uint32(p[12:16])
		dur := time.Duration(binary.BigEndian.Uint32(p[16:20])) * time.Millisecond
		if kbps == 0 || kbps > udpMaxKbps || dur <= 0 || dur > udpMaxDur {
			return
		}
		a.us.mu.Lock()
		if a.us.tx == nil {
			a.us.tx = map[string]bool{}
		}
		busy := a.us.tx[session] || len(a.us.tx) >= 2
		if !busy {
			a.us.tx[session] = true
		}
		a.us.mu.Unlock()
		if busy {
			return // already streaming (the request is sent several times)
		}
		sess := append([]byte(nil), p[4:12]...)
		to := *from
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), dur+5*time.Second)
			defer cancel()
			sendStream(ctx, func(b []byte) error { _, err := uc.WriteToUDP(b, &to); return err }, sess, float64(kbps)/1000, dur, nil)
			time.Sleep(10 * time.Second) // ignore late duplicate requests
			a.us.mu.Lock()
			delete(a.us.tx, session)
			a.us.mu.Unlock()
		}()
	}
}

// ---------------------------------------------------------------- client side

func newSession() ([]byte, string) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return b, hex.EncodeToString(b)
}

func (a *App) dialUDP(host string, port int) (*net.UDPConn, error) {
	ra, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	var la *net.UDPAddr
	if ip := net.ParseIP(a.localBind()); ip != nil {
		la = &net.UDPAddr{IP: ip}
	}
	uc, err := net.DialUDP("udp", la, ra)
	if err != nil {
		return nil, err
	}
	_ = uc.SetReadBuffer(4 << 20)
	_ = uc.SetWriteBuffer(4 << 20)
	return uc, nil
}

// udpStreamDown asks the far end to stream to us and measures what arrives.
func (a *App) udpStreamDown(ctx context.Context, host string, port int, rateMbps float64) (*UDPStats, error) {
	uc, err := a.dialUDP(host, port)
	if err != nil {
		return nil, err
	}
	defer uc.Close()
	sess, _ := newSession()
	req := make([]byte, 20)
	copy(req, "RBWQ")
	copy(req[4:12], sess)
	binary.BigEndian.PutUint32(req[12:16], uint32(rateMbps*1000))
	binary.BigEndian.PutUint32(req[16:20], uint32(udpStreamDur/time.Millisecond))

	rx := newStreamRx()
	start := time.Now()
	gotAny := false
	lastReq := time.Time{}
	lastTick, lastBytes := start, int64(0)
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		if !gotAny {
			if time.Since(start) > 3*time.Second {
				return nil, errors.New("no UDP stream arrived (UDP blocked by a firewall or the VPN?)")
			}
			if time.Since(lastReq) > 300*time.Millisecond {
				_, _ = uc.Write(req)
				lastReq = time.Now()
			}
		}
		_ = uc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := uc.Read(buf)
		now := time.Now()
		if err == nil && n >= 12 && string(buf[4:12]) == string(sess) {
			switch string(buf[:4]) {
			case "RBWS":
				if n >= 24 {
					gotAny = true
					rx.add(binary.BigEndian.Uint32(buf[12:16]), int64(binary.BigEndian.Uint64(buf[16:24])), n, now)
				}
			case "RBWE":
				if n >= 16 {
					rx.end(int(binary.BigEndian.Uint32(buf[12:16])))
				}
			}
		}
		if now.Sub(lastTick) >= 250*time.Millisecond {
			rx.mu.Lock()
			b := rx.bytes
			rx.mu.Unlock()
			a.tick(float64(b-lastBytes)*8/now.Sub(lastTick).Seconds()/1e6, -1)
			lastTick, lastBytes = now, b
		}
		rx.mu.Lock()
		endAt := rx.endAt
		rx.mu.Unlock()
		if !endAt.IsZero() && now.Sub(endAt) > 300*time.Millisecond {
			break
		}
		if gotAny && now.Sub(start) > udpStreamDur+4*time.Second {
			break // end packets lost: use what we have
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	st := rx.stats(-1)
	return &st, nil
}

// udpStreamUp streams to the far end and asks it what arrived.
func (a *App) udpStreamUp(ctx context.Context, host string, port int, rateMbps float64) (*UDPStats, error) {
	uc, err := a.dialUDP(host, port)
	if err != nil {
		return nil, err
	}
	defer uc.Close()
	sess, sessHex := newSession()
	start := time.Now()
	lastTick, lastSent := start, 0
	sent := sendStream(ctx, func(b []byte) error { _, err := uc.Write(b); return err }, sess, rateMbps, udpStreamDur, func(n int) {
		if now := time.Now(); now.Sub(lastTick) >= 250*time.Millisecond {
			a.tick(float64(n-lastSent)*udpPayload*8/now.Sub(lastTick).Seconds()/1e6, -1)
			lastTick, lastSent = now, n
		}
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	time.Sleep(500 * time.Millisecond) // let stragglers land
	c, br, err := a.dialOp(ctx, host, port, Request{Op: "udpstats", Session: sessHex, Sent: sent})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var st UDPStats
	if err := readJSONLine(br, &st, 1<<16); err != nil {
		return nil, err
	}
	if st.Received == 0 {
		return nil, errors.New("none of the UDP stream arrived (UDP blocked by a firewall or the VPN?)")
	}
	return &st, nil
}

// udpRateFor picks the stream rate: a realistic load for the kind of link,
// but never more than 70% of what TCP just measured, so a slow link is
// tested for loss rather than simply overrun.
func udpRateFor(profile string, downMbps, upMbps float64) float64 {
	rate := map[string]float64{"1g": 100, "100m": 30, "wifi": 30, "vpn": 10, "practice": 100}[profile]
	if rate == 0 {
		rate = 10
	}
	if lo := math.Min(downMbps, upMbps); lo > 0 {
		rate = math.Min(rate, lo*0.7)
	}
	if rate < 0.5 {
		rate = 0.5
	}
	return math.Round(rate*10) / 10
}

// pace sleeps just long enough to keep "sent" bytes since start at or below
// mbps (0 = no limit). Used when the far end is a radio with a speed limit.
func pace(start time.Time, sent int64, mbps float64) {
	if mbps <= 0 {
		return
	}
	due := time.Duration(float64(sent) * 8 / (mbps * 1e6) * float64(time.Second))
	if wait := due - time.Since(start); wait > 0 {
		time.Sleep(wait)
	}
}
