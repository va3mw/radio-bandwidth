package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The hard coded test plan. Total is about 45 seconds.
const (
	latencyDur  = 6 * time.Second
	latencyIvl  = 20 * time.Millisecond // 50 probes per second
	throughDur  = 10 * time.Second      // each direction
	warmup      = 1 * time.Second       // ignore TCP ramp-up
	streams     = 4                     // parallel TCP streams
	loadedPing  = 100 * time.Millisecond
	connTimeout = 4 * time.Second
)

type Sample struct {
	T     float64 `json:"t"` // seconds since test start
	Mbps  float64 `json:"mbps,omitempty"`
	RTT   float64 `json:"rtt,omitempty"`
	Phase string  `json:"phase"`
}

type TestState struct {
	Running     bool      `json:"running"`
	Target      string    `json:"target"`
	Phase       string    `json:"phase"` // connect, latency, download, upload, done
	PhaseLabel  string    `json:"phaseLabel"`
	Progress    float64   `json:"progress"`
	CurrentMbps float64   `json:"currentMbps"`
	LastRTT     float64   `json:"lastRtt"`
	Samples     []Sample  `json:"samples"`
	Error       string    `json:"error,omitempty"`
	started     time.Time `json:"-"`
}

const planSeconds = 6.0 + 1.5 + 10 + 10 + 8 + 8 + 3

func (a *App) setPhase(phase, label string) {
	a.mu.Lock()
	a.test.Phase, a.test.PhaseLabel, a.test.CurrentMbps = phase, label, 0
	a.mu.Unlock()
}

func (a *App) tick(mbps, rtt float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.test.Running {
		return // e.g. updating another computer from the list
	}
	t := time.Since(a.test.started).Seconds()
	a.test.Progress = math.Min(t/planSeconds, 0.99)
	if mbps >= 0 {
		a.test.CurrentMbps = mbps
	}
	if rtt > 0 {
		a.test.LastRTT = rtt
	}
	if len(a.test.Samples) < 5000 {
		a.test.Samples = append(a.test.Samples, Sample{T: t, Mbps: math.Max(mbps, 0), RTT: rtt, Phase: a.test.Phase})
	}
}

func (a *App) snapshotTest() TestState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.test
	st.Samples = append([]Sample(nil), a.test.Samples...)
	return st
}

func (a *App) dialer(timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if ip := net.ParseIP(a.localBind()); ip != nil {
		d.LocalAddr = &net.TCPAddr{IP: ip}
	}
	return d
}

// localBind is the chosen interface address, except during a practice run
// over loopback where binding to a LAN address would fail.
func (a *App) localBind() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.noBind {
		return ""
	}
	return a.bindIP
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *App) dialOp(ctx context.Context, host string, port int, req Request) (net.Conn, *bufio.Reader, error) {
	c, err := a.dialer(connTimeout).DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, nil, err
	}
	req.Magic = magic
	if err := writeJSONLine(c, req); err != nil {
		c.Close()
		return nil, nil, err
	}
	return c, bufio.NewReaderSize(c, 256*1024), nil
}

func (a *App) hello(host string, port int, timeout time.Duration) (*HelloReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.helloOp(ctx, host, port, "hello")
}

func (a *App) helloOp(ctx context.Context, host string, port int, op string) (*HelloReply, error) {
	c, br, err := a.dialOp(ctx, host, port, Request{Op: op, FromHost: a.hostName()})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else {
		_ = c.SetDeadline(time.Now().Add(connTimeout))
	}
	var h HelloReply
	if err := readJSONLine(br, &h, 1<<16); err != nil {
		return nil, err
	}
	if h.Magic != magic {
		return nil, errors.New("not a Radio Bandwidth program")
	}
	return &h, nil
}

// runTest is the whole client side: connect, latency, download, upload, grade.
func (a *App) runTest(parent context.Context, target string) (*Result, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.mu.Lock()
	if a.test.Running {
		a.mu.Unlock()
		return nil, errors.New("a test is already running")
	}
	a.test = TestState{Running: true, Target: target, Phase: "connect", PhaseLabel: "Connecting", started: time.Now()}
	a.cancel = cancel
	a.mu.Unlock()

	res, err := a.doTest(ctx, target)

	a.mu.Lock()
	a.test.Running = false
	a.cancel = nil
	if err != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			err = errors.New("test cancelled")
		}
		a.test.Error = err.Error()
		a.test.Phase, a.test.PhaseLabel = "error", "Failed"
	} else {
		a.test.Phase, a.test.PhaseLabel, a.test.Progress = "done", "Finished", 1
		a.lastRes = res
	}
	a.mu.Unlock()
	if err == nil && !res.Practice {
		a.recent.add(Recent{Target: strings.TrimSpace(target), Host: res.RemoteHost, IP: res.RemoteIP, Verdict: res.Verdict})
	}
	return res, err
}

func (a *App) doTest(ctx context.Context, target string) (*Result, error) {
	host, port := a.splitTarget(target)
	if host == "" {
		return nil, errors.New("enter the other computer's IP address or name")
	}
	a.setPhase("connect", "Finding "+host)
	ip, typedName, err := resolveTarget(ctx, host)
	if err != nil {
		return nil, err
	}
	host = ip
	practice := isLoopbackHost(host)
	a.mu.Lock()
	a.noBind = practice
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.noBind = false; a.mu.Unlock() }()

	h, err := a.helloOp(ctx, host, port, "hello")
	if err != nil {
		where := host
		if typedName != "" {
			where = typedName + " (" + host + ")"
		}
		return nil, fmt.Errorf("could not reach Radio Bandwidth on %s. Make sure Radio Bandwidth is running on that computer, the address is right, and the firewall allows it (%v)", where, err)
	}
	self := h.ID == a.id
	// Versions: update an older far end if we can, else test what it supports.
	var versionNotes []string
	if !self && !practice {
		h, versionNotes = a.reconcileVersions(ctx, host, port, h)
	}
	feats := peerFeatures(h)
	a.mu.Lock()
	a.capMbps = h.MaxMbps
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.capMbps = 0; a.mu.Unlock() }()
	if h.Busy && !self {
		return nil, fmt.Errorf("%s is busy running its own test. Try again in a moment", h.Host)
	}
	if !self {
		if _, err := a.helloOp(ctx, host, port, "start"); err != nil {
			return nil, err
		}
	}

	r := &Result{
		Time:       time.Now().Format("2006-01-02 15:04:05"),
		Version:    appVersion,
		LocalHost:  a.hostName(),
		RemoteHost: h.Host,
		RemoteIP:   host,
		RemoteName: typedName,
		RemoteOS:   h.OS,
		RemoteKind: h.Kind, RemoteIface: h.Iface, RemoteLinkMbps: h.LinkMbps, RemoteDuplex: h.Duplex,
		Practice:      practice,
		RateCapMbps:   h.MaxMbps,
		RemoteVersion: h.Version,
		VersionNotes:  versionNotes,
	}
	if h.Radio != nil {
		r.RemoteRadio = h.Radio.Model
		if r.RemoteRadio == "" {
			r.RemoteRadio = "FlexRadio"
		}
	}
	if practice {
		r.RemoteHost = h.Host + " (itself)"
		if !self {
			r.RemoteHost = h.Host + " (" + copyName(port) + ")"
		}
	}
	// Which local connection is actually carrying the test?
	a.mu.Lock()
	r.LocalIP = a.bindIP
	a.mu.Unlock()
	if practice {
		r.LocalIP = "127.0.0.1"
	} else if r.LocalIP == "" {
		r.LocalIP = localIPFor(host)
	}
	if !practice {
		if onLocalSubnet(host) {
			r.Route = "local"
		} else {
			r.Route = "routed"
		}
	}
	if ifc, ok := a.ifaceForIP(r.LocalIP); ok {
		r.LocalIface, r.LocalKind, r.LocalLinkMbps, r.LocalDuplex = ifc.Name, ifc.Kind, ifc.LinkMbps, ifc.Duplex
	}

	// 1. Idle latency, jitter and loss.
	a.setPhase("latency", "Measuring delay and jitter")
	lat, err := a.measureLatency(ctx, host, port, latencyDur, latencyIvl)
	if err != nil {
		return nil, err
	}
	r.LatencyMethod = lat.method
	r.IdleRTT, r.IdleRTTMin, r.IdleRTTMax, r.JitterMs = lat.mean(), lat.min(), lat.max(), lat.jitter()
	r.Sent, r.Lost = lat.sent, lat.lost()
	r.LossPct = lat.lossPct()

	// 1b. Largest packet that gets through (MTU), and fragment handling.
	if !feats["mtu"] {
		r.Skipped = append(r.Skipped, "mtu")
	} else if lat.method == "UDP" {
		a.setPhase("mtu", "Checking packet size (MTU)")
		r.MTU = a.mtuTest(ctx, host, port)
		a.tick(-1, -1)
	}

	// 2. Download (far end sends to us), with a slow latency probe alongside.
	a.setPhase("download", "Download test ("+h.Host+" → "+a.hostName()+")")
	r.DownMbps, r.DownLoadedRTT, err = a.throughputWithProbe(ctx, host, port, "download", lat.method)
	if err != nil {
		return nil, err
	}
	// 3. Upload.
	a.setPhase("upload", "Upload test ("+a.hostName()+" → "+h.Host+")")
	r.UpMbps, r.UpLoadedRTT, err = a.throughputWithProbe(ctx, host, port, "upload", lat.method)
	if err != nil {
		return nil, err
	}

	// 4 and 5. Fixed-rate UDP streams, like radio audio, panadapter and IQ data.
	a.mu.Lock()
	prof := a.profile
	a.mu.Unlock()
	streamProf := prof
	if practice {
		streamProf = "practice"
	} else if streamProf == "" || streamProf == "auto" {
		streamProf, _ = r.autoProfile()
	}
	r.UDPRateMbps = udpRateFor(streamProf, r.DownMbps, r.UpMbps)
	if h.MaxMbps > 0 && r.UDPRateMbps > h.MaxMbps {
		r.UDPRateMbps = h.MaxMbps
	}
	if !feats["udpstream"] {
		r.Skipped = append(r.Skipped, "udpstream")
	} else if lat.method != "UDP" {
		r.UDPNote = "UDP is blocked between these computers, so the UDP stream test could not run. Ham radio audio and SDR streams will not work through this path either."
	} else {
		a.setPhase("udpdown", fmt.Sprintf("UDP stream %s → %s at %s Mb/s", h.Host, a.hostName(), f1(r.UDPRateMbps)))
		if st, err := a.udpStreamDown(ctx, host, port, r.UDPRateMbps); err == nil {
			r.UDPDown = st
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		} else {
			r.UDPNote = "UDP stream " + h.Host + " → " + a.hostName() + ": " + err.Error()
		}
		a.setPhase("udpup", fmt.Sprintf("UDP stream %s → %s at %s Mb/s", a.hostName(), h.Host, f1(r.UDPRateMbps)))
		if st, err := a.udpStreamUp(ctx, host, port, r.UDPRateMbps); err == nil {
			r.UDPUp = st
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		} else {
			r.UDPNote = strings.TrimSpace(r.UDPNote + " UDP stream " + a.hostName() + " → " + h.Host + ": " + err.Error())
		}
	}

	r.Grade(prof)

	// Tell the far end so its screen shows the same results.
	if self {
		return r, nil
	}
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	if c, br, err := a.dialOp(rctx, host, port, Request{Op: "report", FromHost: a.hostName(), Result: r}); err == nil {
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = br.ReadString('\n')
		c.Close()
	}
	rcancel()
	return r, nil
}

func (a *App) throughputWithProbe(ctx context.Context, host string, port int, dir, method string) (float64, float64, error) {
	pctx, pcancel := context.WithCancel(ctx)
	probeDone := make(chan *latStats, 1)
	go func() {
		// Wait for the pipe to fill before probing.
		select {
		case <-time.After(warmup):
		case <-pctx.Done():
			probeDone <- &latStats{}
			return
		}
		st, _ := a.measureLatencyMethod(pctx, host, port, throughDur, loadedPing, method, false)
		probeDone <- st
	}()
	mbps, err := a.throughput(ctx, host, port, dir)
	pcancel()
	st := <-probeDone
	if err != nil {
		return 0, 0, err
	}
	return mbps, st.mean(), nil
}

// throughput runs parallel TCP streams in one direction and returns Mb/s,
// leaving out the first second while TCP ramps up.
func (a *App) throughput(ctx context.Context, host string, port int, dir string) (float64, error) {
	var live atomic.Int64 // bytes moved, for the live display
	conns := make([]net.Conn, 0, streams)
	readers := make([]*bufio.Reader, 0, streams)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < streams; i++ {
		c, br, err := a.dialOp(ctx, host, port, Request{Op: dir, DurationMs: int(throughDur / time.Millisecond), FromHost: a.hostName()})
		if err != nil {
			return 0, fmt.Errorf("could not open test connection: %v", err)
		}
		tuneTCP(c)
		conns = append(conns, c)
		readers = append(readers, br)
	}
	go func() {
		<-ctx.Done()
		for _, c := range conns {
			c.Close()
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	var lastByte atomic.Int64 // ns since start of the last received byte (download)
	upReplies := make([]UploadReply, streams)
	errs := make([]error, streams)

	for i := range conns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, br := conns[i], readers[i]
			if dir == "download" {
				_ = c.SetReadDeadline(time.Now().Add(throughDur + 15*time.Second))
				buf := make([]byte, 256*1024)
				for {
					n, err := br.Read(buf)
					if n > 0 {
						live.Add(int64(n))
						lastByte.Store(int64(time.Since(start)))
					}
					if err != nil {
						return
					}
				}
			}
			buf := make([]byte, 128*1024)
			_, _ = rand.Read(buf)
			end := start.Add(throughDur)
			_ = c.SetWriteDeadline(end.Add(10 * time.Second))
			a.mu.Lock()
			perStream := a.capMbps / streams
			a.mu.Unlock()
			var sent int64
			for time.Now().Before(end) {
				n, err := c.Write(buf)
				live.Add(int64(n))
				sent += int64(n)
				if err != nil {
					errs[i] = err
					return
				}
				pace(start, sent, perStream)
			}
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
			_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
			errs[i] = readJSONLine(br, &upReplies[i], 1<<16)
		}(i)
	}

	// Live updates while the streams run.
	done := make(chan struct{})
	tickerExited := make(chan struct{})
	var warmBytes int64
	var warmAt time.Duration
	go func() {
		defer close(tickerExited)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		prev, prevT := int64(0), time.Duration(0)
		for {
			select {
			case <-done:
				return
			case <-t.C:
				now := time.Since(start)
				cur := live.Load()
				if warmAt == 0 && now >= warmup {
					warmBytes, warmAt = cur, now
				}
				mbps := float64(cur-prev) * 8 / (now - prevT).Seconds() / 1e6
				prev, prevT = cur, now
				a.tick(mbps, -1)
			}
		}
	}()
	wg.Wait()
	close(done)
	<-tickerExited
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	if dir == "download" {
		total := live.Load()
		end := time.Duration(lastByte.Load())
		if warmAt == 0 || end-warmAt < 500*time.Millisecond {
			if end <= 0 {
				return 0, errors.New("no data was received")
			}
			return float64(total) * 8 / end.Seconds() / 1e6, nil
		}
		return float64(total-warmBytes) * 8 / (end - warmAt).Seconds() / 1e6, nil
	}
	var mbps float64
	got := 0
	for i, rep := range upReplies {
		if errs[i] != nil || rep.MeasuredSec <= 0 {
			continue
		}
		mbps += float64(rep.MeasuredBytes) * 8 / rep.MeasuredSec / 1e6
		got++
	}
	if got == 0 {
		for _, e := range errs {
			if e != nil {
				return 0, fmt.Errorf("upload test failed: %v", e)
			}
		}
		return 0, errors.New("upload test failed: no data arrived")
	}
	return mbps, nil
}

// ---------------------------------------------------------------- latency

type latStats struct {
	method string
	sent   int
	rtts   []float64 // ms, in arrival order
}

func (s *latStats) mean() float64 {
	if s == nil || len(s.rtts) == 0 {
		return 0
	}
	t := 0.0
	for _, v := range s.rtts {
		t += v
	}
	return t / float64(len(s.rtts))
}
func (s *latStats) min() float64 {
	if len(s.rtts) == 0 {
		return 0
	}
	m := s.rtts[0]
	for _, v := range s.rtts {
		m = math.Min(m, v)
	}
	return m
}
func (s *latStats) max() float64 {
	m := 0.0
	for _, v := range s.rtts {
		m = math.Max(m, v)
	}
	return m
}

// jitter is the mean difference between consecutive delay samples
// (the same idea as RFC 3550 and iPerf).
func (s *latStats) jitter() float64 {
	if len(s.rtts) < 2 {
		return 0
	}
	t := 0.0
	for i := 1; i < len(s.rtts); i++ {
		t += math.Abs(s.rtts[i] - s.rtts[i-1])
	}
	return t / float64(len(s.rtts)-1)
}
func (s *latStats) lost() int {
	if s.method != "UDP" {
		return 0
	}
	l := s.sent - len(s.rtts)
	if l < 0 {
		return 0
	}
	return l
}
func (s *latStats) lossPct() float64 {
	if s.sent == 0 || s.method != "UDP" {
		return 0
	}
	return float64(s.lost()) * 100 / float64(s.sent)
}
func (s *latStats) percentile(p float64) float64 {
	if len(s.rtts) == 0 {
		return 0
	}
	c := append([]float64(nil), s.rtts...)
	sort.Float64s(c)
	return c[int(math.Min(float64(len(c)-1), p*float64(len(c))))]
}

// measureLatency tries UDP first (so packet loss can be measured) and falls
// back to TCP if a firewall eats the UDP packets.
func (a *App) measureLatency(ctx context.Context, host string, port int, dur, ivl time.Duration) (*latStats, error) {
	st, err := a.measureLatencyMethod(ctx, host, port, dur, ivl, "UDP", true)
	if err == nil && len(st.rtts) > 0 {
		return st, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return a.measureLatencyMethod(ctx, host, port, dur, ivl, "TCP", true)
}

var errNoUDP = errors.New("no UDP replies")

func (a *App) measureLatencyMethod(ctx context.Context, host string, port int, dur, ivl time.Duration, method string, show bool) (*latStats, error) {
	if method == "TCP" {
		return a.tcpPing(ctx, host, port, dur, ivl, show)
	}
	return a.udpPing(ctx, host, port, dur, ivl, show)
}

func (a *App) udpPing(ctx context.Context, host string, port int, dur, ivl time.Duration, show bool) (*latStats, error) {
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
	defer uc.Close()

	st := &latStats{method: "UDP"}
	start := time.Now()
	var mu sync.Mutex
	seen := map[uint32]bool{}
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		buf := make([]byte, 2048)
		for {
			_ = uc.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			n, err := uc.Read(buf)
			if err != nil {
				mu.Lock()
				sending := st.sent > 0 && time.Since(start) < dur
				mu.Unlock()
				if ne, ok := err.(net.Error); ok && ne.Timeout() && sending {
					continue
				}
				return
			}
			if n < 16 || string(buf[:4]) != "RBWF" {
				continue
			}
			seq := binary.BigEndian.Uint32(buf[4:8])
			sentAt := time.Duration(binary.BigEndian.Uint64(buf[8:16]))
			rtt := float64(time.Since(start)-sentAt) / 1e6
			mu.Lock()
			if !seen[seq] {
				seen[seq] = true
				st.rtts = append(st.rtts, rtt)
			}
			mu.Unlock()
			if show {
				a.tick(-1, rtt)
			}
		}
	}()

	pkt := make([]byte, 64)
	copy(pkt, "RBWF")
	t := time.NewTicker(ivl)
	defer t.Stop()
	var seq uint32
	checked := false
loop:
	for time.Since(start) < dur {
		select {
		case <-ctx.Done():
			break loop
		case <-t.C:
		}
		binary.BigEndian.PutUint32(pkt[4:8], seq)
		binary.BigEndian.PutUint64(pkt[8:16], uint64(time.Since(start)))
		seq++
		_, _ = uc.Write(pkt)
		mu.Lock()
		st.sent = int(seq)
		got := len(st.rtts)
		mu.Unlock()
		if show && !checked && time.Since(start) > 1500*time.Millisecond {
			checked = true
			if got == 0 {
				return st, errNoUDP // firewall: switch to TCP
			}
		}
		if show {
			a.tick(-1, -1)
		}
	}
	<-recvDone // waits up to 1.5 s for late replies
	mu.Lock()
	defer mu.Unlock()
	return st, nil
}

func (a *App) tcpPing(ctx context.Context, host string, port int, dur, ivl time.Duration, show bool) (*latStats, error) {
	c, br, err := a.dialOp(ctx, host, port, Request{Op: "ping"})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	go func() { <-ctx.Done(); c.Close() }()
	st := &latStats{method: "TCP"}
	start := time.Now()
	b := make([]byte, 16)
	rb := make([]byte, 16)
	for time.Since(start) < dur && ctx.Err() == nil {
		t0 := time.Now()
		binary.BigEndian.PutUint64(b, uint64(st.sent))
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(b); err != nil {
			break
		}
		st.sent++
		if _, err := ioReadFull(br, rb); err != nil {
			break
		}
		rtt := float64(time.Since(t0)) / 1e6
		st.rtts = append(st.rtts, rtt)
		if show {
			a.tick(-1, rtt)
		}
		if s := ivl - time.Since(t0); s > 0 {
			time.Sleep(s)
		}
	}
	if len(st.rtts) == 0 && ctx.Err() == nil {
		return st, errors.New("latency test failed: no replies")
	}
	return st, nil
}

func ioReadFull(br *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := br.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
