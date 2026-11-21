package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Request is the first JSON line on every TCP connection.
type Request struct {
	Magic      string  `json:"magic"`
	Op         string  `json:"op"` // hello, start, download, upload, ping, report
	DurationMs int     `json:"durationMs,omitempty"`
	FromHost   string  `json:"fromHost,omitempty"`
	Result     *Result `json:"result,omitempty"`
	Session    string  `json:"session,omitempty"`
	Sent       int     `json:"sent,omitempty"`
	Version    string  `json:"version,omitempty"` // update: at least this version
}

// HelloReply describes the far end.
type HelloReply struct {
	Magic       string     `json:"magic"`
	ID          string     `json:"id"`
	Proto       int        `json:"proto,omitempty"`       // wire protocol version (0 = before 1.7.0)
	Features    []string   `json:"features,omitempty"`    // tests and functions it supports
	UpdateState string     `json:"updateState,omitempty"` // progress of an update it was asked to do
	Radio       *RadioInfo `json:"radio,omitempty"`       // the far end is a FlexRadio
	MaxMbps     float64    `json:"maxMbps,omitempty"`     // keep test traffic under this
	App         string     `json:"app"`
	Version     string     `json:"version"`
	Host        string     `json:"host"`
	OS          string     `json:"os"`
	Busy        bool       `json:"busy"`
	Iface       string     `json:"iface"`
	Kind        string     `json:"kind"`
	LinkMbps    int        `json:"linkMbps"`
	Duplex      string     `json:"duplex,omitempty"`
}

type UploadReply struct {
	Bytes         int64   `json:"bytes"`
	MeasuredBytes int64   `json:"measuredBytes"`
	MeasuredSec   float64 `json:"measuredSec"`
}

type Peer struct {
	ID       string    `json:"id"`
	Addr     string    `json:"addr"` // host or host:port to test against
	IP       string    `json:"ip"`
	Host     string    `json:"host"`
	OS       string    `json:"os"`
	Via      string    `json:"via"`               // "LAN" or "Tailscale"
	DNS      string    `json:"dns,omitempty"`     // MagicDNS name
	Radio    string    `json:"radio,omitempty"`   // FlexRadio model, if it is one
	Version  string    `json:"version,omitempty"` // Radio Bandwidth version it runs, once known
	Update   string    `json:"update,omitempty"`  // see peerUpdateInfo
	LastSeen time.Time `json:"-"`
}

// Incoming describes a test someone else is running against us.
type Incoming struct {
	FromHost string    `json:"fromHost"`
	FromIP   string    `json:"fromIP"`
	Started  time.Time `json:"started"`
	Result   *Result   `json:"result,omitempty"`
}

type App struct {
	mu       sync.Mutex
	id       string
	host     string
	listenIP string
	port     int
	discPort int
	bindIP   string
	profile  string
	noBind   bool // true while testing over loopback (practice)

	peers    map[string]*Peer
	test     TestState
	cancel   context.CancelFunc
	incoming *Incoming
	lastRes  *Result
	recent   recentStore

	upd         updater
	afterUpdate bool   // just restarted by an update: wait for the ports
	latest      string // newer release seen on GitHub, for the banner

	pver map[string]peerVer // other copies' versions, by address:port
	pupd map[string]string  // updates of other copies started from the page

	radio   *RadioInfo // set when running inside a FlexRadio
	maxMbps float64    // responder side: speed limit testers must respect (0 = none)
	capMbps float64    // client side, during a test: the far end's limit

	us udpServer // UDP stream test state

	ifCache   []IfaceInfo
	ifCacheAt time.Time
	disc      *net.UDPConn
}

func newApp(listen string, port, discPort int) *App {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	h, _ := os.Hostname()
	if h == "" {
		h = "unknown"
	}
	a := &App{
		id:       hex.EncodeToString(b),
		host:     h,
		listenIP: listen,
		port:     port,
		discPort: discPort,
		profile:  "auto",
		peers:    map[string]*Peer{},
		pver:     map[string]peerVer{},
		pupd:     map[string]string{},
		test:     TestState{PhaseLabel: "Ready"},
	}
	a.recent.load()
	return a
}

func (a *App) startServers() error {
	addr := net.JoinHostPort(a.listenIP, fmt.Sprint(a.port))
	var ln net.Listener
	var uc *net.UDPConn
	var err error
	// Right after an update the old copy may still be letting go of the
	// ports, so keep trying for a while.
	for try := 0; ; try++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			ua, _ := net.ResolveUDPAddr("udp", addr)
			if uc, err = net.ListenUDP("udp", ua); err == nil {
				break
			}
			ln.Close()
		}
		if !a.afterUpdate || try >= 40 {
			return fmt.Errorf("port %d is in use (%v)", a.port, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	go a.serveTCP(ln)
	go a.serveUDPEcho(uc)
	return nil
}

func (a *App) serveTCP(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go a.handle(c)
	}
}

func (a *App) serveUDPEcho(uc *net.UDPConn) {
	_ = uc.SetReadBuffer(4 << 20)
	_ = uc.SetWriteBuffer(4 << 20)
	buf := make([]byte, 65536)
	for {
		n, from, err := uc.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if n < 4 {
			continue
		}
		if bytes.Equal(buf[:4], []byte("RBWF")) {
			_, _ = uc.WriteToUDP(buf[:n], from)
			continue
		}
		if buf[0] == 'R' && buf[1] == 'B' && buf[2] == 'W' {
			a.handleStreamPacket(uc, buf[:n], from, time.Now())
		}
	}
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readJSONLine(br *bufio.Reader, v any, max int) error {
	var line []byte
	for {
		part, isPrefix, err := br.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, part...)
		if len(line) > max {
			return errors.New("line too long")
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v)
}

func (a *App) helloReply(local net.Addr) HelloReply {
	r := HelloReply{Magic: magic, ID: a.id, App: appName, Version: appVersion, Host: a.hostName(), OS: runtime.GOOS}
	a.mu.Lock()
	r.Busy = a.test.Running
	r.Radio, r.MaxMbps = a.radio, a.maxMbps
	a.mu.Unlock()
	r.Proto, r.Features, r.UpdateState = protoVersion, myFeatures, a.upd.get()
	if ta, ok := local.(*net.TCPAddr); ok {
		if ifc, ok := a.ifaceForIP(ta.IP.String()); ok {
			r.Iface, r.Kind, r.LinkMbps, r.Duplex = ifc.Name, ifc.Kind, ifc.LinkMbps, ifc.Duplex
		}
	}
	return r
}

func remoteIP(c net.Conn) string {
	if ta, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return ta.IP.String()
	}
	return c.RemoteAddr().String()
}

func (a *App) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(c, 64*1024)
	var req Request
	if err := readJSONLine(br, &req, 1<<20); err != nil || req.Magic != magic {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	a.us.allow(remoteIP(c))
	dur := time.Duration(req.DurationMs) * time.Millisecond
	if dur < time.Second {
		dur = time.Second
	}
	if dur > 30*time.Second {
		dur = 30 * time.Second
	}

	switch req.Op {
	case "hello":
		_ = writeJSONLine(c, a.helloReply(c.LocalAddr()))

	case "update": // the tester has a newer version: update ourselves
		type reply struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason,omitempty"`
		}
		var rep reply
		if compareVersions(req.Version, appVersion) <= 0 {
			rep.Reason = "already running " + appVersion
		} else if err := a.startUpdate(req.Version, "asked by "+req.FromHost); err != nil {
			rep.Reason = err.Error()
		} else {
			rep.OK = true
		}
		_ = writeJSONLine(c, rep)

	case "udpstats": // results of a UDP stream the other side sent us
		st := UDPStats{Sent: req.Sent, LossPct: 100}
		if rx := a.us.take(req.Session); rx != nil {
			st = rx.stats(req.Sent)
		}
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = writeJSONLine(c, st)

	case "start":
		a.mu.Lock()
		a.incoming = &Incoming{FromHost: req.FromHost, FromIP: remoteIP(c), Started: time.Now()}
		a.mu.Unlock()
		fmt.Printf("[%s] Test started by %s (%s)\n", time.Now().Format("15:04:05"), req.FromHost, remoteIP(c))
		_ = writeJSONLine(c, a.helloReply(c.LocalAddr()))

	case "report":
		if req.Result == nil {
			return
		}
		a.mu.Lock()
		if a.incoming == nil {
			a.incoming = &Incoming{FromHost: req.FromHost, FromIP: remoteIP(c), Started: time.Now()}
		}
		a.incoming.Result = req.Result
		a.mu.Unlock()
		if ip := net.ParseIP(remoteIP(c)); ip != nil && !ip.IsLoopback() {
			a.recent.add(Recent{Target: remoteIP(c), Host: req.FromHost, IP: remoteIP(c)})
		}
		fmt.Printf("[%s] Results from %s:\n%s\n", time.Now().Format("15:04:05"), req.FromHost, req.Result.Text())
		_, _ = c.Write([]byte("{}\n"))

	case "download": // we send, they receive
		tuneTCP(c)
		buf := make([]byte, 128*1024)
		_, _ = rand.Read(buf)
		start := time.Now()
		end := start.Add(dur)
		_ = c.SetWriteDeadline(end.Add(5 * time.Second))
		a.mu.Lock()
		perStream := a.maxMbps / streams
		a.mu.Unlock()
		var sent int64
		for time.Now().Before(end) {
			n, err := c.Write(buf)
			if err != nil {
				return
			}
			sent += int64(n)
			pace(start, sent, perStream)
		}

	case "upload": // they send, we receive and report what actually arrived
		tuneTCP(c)
		_ = c.SetReadDeadline(time.Now().Add(dur + 20*time.Second))
		buf := make([]byte, 128*1024)
		var total, atWarm int64
		var first, warm time.Time
		for {
			n, err := br.Read(buf)
			if n > 0 {
				now := time.Now()
				if first.IsZero() {
					first = now
				}
				total += int64(n)
				if warm.IsZero() && now.Sub(first) >= warmup {
					warm, atWarm = now, total
				}
			}
			if err != nil {
				break
			}
		}
		end := time.Now()
		rep := UploadReply{Bytes: total}
		if !warm.IsZero() && end.Sub(warm) > 200*time.Millisecond {
			rep.MeasuredBytes, rep.MeasuredSec = total-atWarm, end.Sub(warm).Seconds()
		} else if !first.IsZero() {
			rep.MeasuredBytes, rep.MeasuredSec = total, end.Sub(first).Seconds()
		}
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = writeJSONLine(c, rep)

	case "ping": // TCP echo, used when UDP is blocked
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		b := make([]byte, 16)
		for {
			_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
			if _, err := io.ReadFull(br, b); err != nil {
				return
			}
			if _, err := c.Write(b); err != nil {
				return
			}
		}
	}
}

func tuneTCP(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4 << 20)
		_ = tc.SetWriteBuffer(4 << 20)
	}
}

// ---------------------------------------------------------------- discovery

type announce struct {
	Magic string `json:"magic"`
	ID    string `json:"id"`
	Host  string `json:"host"`
	OS    string `json:"os"`
	Port  int    `json:"port"`
	Radio string `json:"radio,omitempty"` // model, when this is a FlexRadio
}

// hostName is the name this copy announces; inside a radio it changes once
// the radio API answers.
func (a *App) hostName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.host
}

// discoveryLoop broadcasts "I'm here" on every LAN and listens for others.
func (a *App) discoveryLoop() {
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: a.discPort})
	if err != nil {
		fmt.Println("Note: automatic LAN discovery is off (", err, "). Type the other computer's address instead.")
		return
	}
	a.disc = uc
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := uc.ReadFromUDP(buf)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var an announce
			if json.Unmarshal(buf[:n], &an) != nil || an.Magic != magic || an.ID == a.id {
				continue
			}
			ip := from.IP.String()
			addr := ip
			if an.Port != 0 && an.Port != defDataPort {
				addr = net.JoinHostPort(ip, fmt.Sprint(an.Port))
			}
			a.addPeer(&Peer{ID: an.ID, Addr: addr, IP: ip, Host: an.Host, OS: an.OS, Via: "LAN", Radio: an.Radio})
			a.refreshPeerVersion(addr)
		}
	}()
	for {
		an := announce{Magic: magic, ID: a.id, Host: a.hostName(), OS: runtime.GOOS, Port: a.port}
		a.mu.Lock()
		if a.radio != nil {
			an.Radio = a.radio.Model
			if an.Radio == "" {
				an.Radio = "FlexRadio"
			}
		}
		a.mu.Unlock()
		msg, _ := json.Marshal(an)
		targets := map[string]bool{"255.255.255.255": true}
		for _, b := range broadcastAddrs() {
			targets[b] = true
		}
		for t := range targets {
			_, _ = uc.WriteToUDP(msg, &net.UDPAddr{IP: net.ParseIP(t), Port: a.discPort})
		}
		time.Sleep(2 * time.Second)
	}
}

func (a *App) addPeer(p *Peer) {
	p.LastSeen = time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	key := p.IP
	if old, ok := a.peers[key]; ok && old.Via == "Tailscale" && p.Via == "LAN" {
		p.Via = old.Via // keep the more specific label
	}
	a.peers[key] = p
}

func (a *App) peerList() []Peer {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Peer
	for k, p := range a.peers {
		max := 8 * time.Second
		if p.Via == "Tailscale" || p.Via == "This computer" {
			max = 60 * time.Second
		}
		if time.Since(p.LastSeen) > max {
			delete(a.peers, k)
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Via != out[j].Via {
			return out[i].Via == "LAN"
		}
		return strings.ToLower(out[i].Host) < strings.ToLower(out[j].Host)
	})
	return out
}

// ---------------------------------------------------------------- tailscale

// tailscaleLoop finds Tailscale peers (broadcast does not cross VPNs) and
// checks which of them are running Radio Bandwidth.
func (a *App) tailscaleLoop() {
	if !tailscaleInstalled() {
		return
	}
	for {
		a.scanTailscale()
		time.Sleep(20 * time.Second)
	}
}

func (a *App) scanTailscale() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	peers, err := tailscalePeers(ctx)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range peers {
		if !p.Online || p.IPv4 == "" {
			continue
		}
		wg.Add(1)
		go func(p tsPeer) {
			defer wg.Done()
			h, err := a.hello(p.IPv4, a.port, 1500*time.Millisecond)
			if err != nil {
				return
			}
			a.notePeerVersion(a.verKey(p.IPv4), h)
			a.addPeer(&Peer{ID: "ts-" + p.IPv4, Addr: p.IPv4, IP: p.IPv4, Host: h.Host, OS: h.OS, Via: "Tailscale", DNS: p.DNSName})
		}(p)
	}
	wg.Wait()
}

// practicePeerLoop lists a second copy running on this same computer
// (started with --second) so new users can practise with two windows.
func (a *App) practicePeerLoop() {
	other := defDataPort
	if a.port == defDataPort {
		other = secondDataPort
	}
	for {
		if h, err := a.hello("127.0.0.1", other, time.Second); err == nil && h.ID != a.id {
			a.notePeerVersion(a.verKey(fmt.Sprintf("127.0.0.1:%d", other)), h)
			a.addPeer(&Peer{ID: h.ID, Addr: fmt.Sprintf("127.0.0.1:%d", other), IP: fmt.Sprintf("127.0.0.1:%d", other),
				Host: h.Host + " (" + copyName(other) + ")", OS: h.OS, Via: "This computer"})
		}
		time.Sleep(3 * time.Second)
	}
}

func copyName(port int) string {
	if port == secondDataPort {
		return "second copy"
	}
	return "first copy"
}

// isOurUI checks whether another copy already serves the UI there.
func isOurUI(url string) bool {
	c := &httpClient
	resp, err := c.Get(url + "api/ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	return strings.Contains(string(b), appName)
}
