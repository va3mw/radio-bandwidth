package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Profile is a hard coded "what good looks like" for one kind of connection.
type Profile struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	ExpectMbps float64 `json:"expectMbps"` // typical real TCP throughput, 0 = no fixed expectation
	ThrGood    float64 `json:"thrGood"`
	ThrFair    float64 `json:"thrFair"`
	RTTGood    float64 `json:"rttGood"`
	RTTFair    float64 `json:"rttFair"`
	JitGood    float64 `json:"jitGood"`
	JitFair    float64 `json:"jitFair"`
	LossGood   float64 `json:"lossGood"`
	LossFair   float64 `json:"lossFair"`
	BloatGood  float64 `json:"bloatGood"` // extra delay under load, ms
	BloatFair  float64 `json:"bloatFair"`
}

// Speed is the one thing whose "normal" depends on the kind of link. A
// healthy wired gigabit link moves about 940 Mb/s of real TCP data (the rest
// is Ethernet, IP and TCP overhead); Fast Ethernet about 94 Mb/s.
//
// ThrGood / ThrFair are deliberately practical: below ThrGood people notice
// slow file copies, below ThrFair the link is clearly broken.
var profiles = map[string]Profile{
	"1g":   {Key: "1g", Name: "1 Gb/s wired LAN", ExpectMbps: 940, ThrGood: 700, ThrFair: 300},
	"100m": {Key: "100m", Name: "100 Mb/s wired LAN", ExpectMbps: 94, ThrGood: 80, ThrFair: 40},
	"wifi": {Key: "wifi", Name: "Wi-Fi", ExpectMbps: 0, ThrGood: 100, ThrFair: 25},
	"vpn":  {Key: "vpn", Name: "VPN / remote site / Internet", ExpectMbps: 0, ThrGood: 10, ThrFair: 3},
}

// What people actually hear and feel. These are the same for every kind of
// link: a radio operator does not care whether 20 ms of jitter came from
// Wi-Fi or a VPN, only that the audio stutters.
const (
	rttFair   = 50   // ms round trip: remote control starts to feel laggy
	rttPoor   = 150  // ms: CW / PTT timing and conversation are clearly affected
	jitFair   = 5    // ms: audio apps need a bigger buffer, occasional stutter
	jitPoor   = 15   // ms: stutter and dropouts with normal buffers
	lossFair  = 0.05 // % of a real-time stream: occasional clicks or gaps
	lossPoor  = 0.5  // %: frequent dropouts
	bloatFair = 50   // ms extra delay while the link is busy: lag you notice
	bloatPoor = 150  // ms: remote operation is hard while anything big transfers

	bloatFairLAN = 100 // on a LAN, only during big copies
)

var profileOrder = []string{"1g", "100m", "wifi", "vpn"}

type Metric struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Grade  string  `json:"grade"` // good, fair, poor, info
	Expect string  `json:"expect"`
}

type Result struct {
	Time    string `json:"time"`
	Version string `json:"version"`

	LocalHost     string `json:"localHost"`
	LocalIP       string `json:"localIP"`
	LocalIface    string `json:"localIface"`
	LocalKind     string `json:"localKind"`
	LocalLinkMbps int    `json:"localLinkMbps"`
	LocalDuplex   string `json:"localDuplex,omitempty"`

	RemoteHost     string `json:"remoteHost"`
	RemoteIP       string `json:"remoteIP"`
	RemoteName     string `json:"remoteName,omitempty"` // what the user typed / MagicDNS name
	RemoteOS       string `json:"remoteOS"`
	RemoteIface    string `json:"remoteIface"`
	RemoteKind     string `json:"remoteKind"`
	RemoteLinkMbps int    `json:"remoteLinkMbps"`
	RemoteDuplex   string `json:"remoteDuplex,omitempty"`
	RemoteRadio    string `json:"remoteRadio,omitempty"` // FlexRadio model when the far end is a radio

	DownMbps      float64  `json:"downMbps"`
	UpMbps        float64  `json:"upMbps"`
	IdleRTT       float64  `json:"idleRtt"`
	IdleRTTMin    float64  `json:"idleRttMin"`
	IdleRTTMax    float64  `json:"idleRttMax"`
	JitterMs      float64  `json:"jitterMs"`
	Sent          int      `json:"sent"`
	Lost          int      `json:"lost"`
	LossPct       float64  `json:"lossPct"`
	DownLoadedRTT float64  `json:"downLoadedRtt"`
	UpLoadedRTT   float64  `json:"upLoadedRtt"`
	LatencyMethod string   `json:"latencyMethod"`
	Practice      bool     `json:"practice"`
	RateCapMbps   float64  `json:"rateCapMbps,omitempty"` // far end limited test traffic to this
	RemoteVersion string   `json:"remoteVersion,omitempty"`
	VersionNotes  []string `json:"versionNotes,omitempty"` // updates done or not possible
	Skipped       []string `json:"skipped,omitempty"`      // tests the far end could not do

	UDPRateMbps float64    `json:"udpRateMbps"`
	UDPDown     *UDPStats  `json:"udpDown,omitempty"`
	UDPUp       *UDPStats  `json:"udpUp,omitempty"`
	UDPNote     string     `json:"udpNote,omitempty"`
	MTU         *MTUResult `json:"mtu,omitempty"`
	Fixes       []Fix      `json:"fixes"`

	ProfileKey  string    `json:"profileKey"`
	ProfileName string    `json:"profileName"`
	ProfileAuto bool      `json:"profileAuto"`
	AutoReason  string    `json:"autoReason"`
	Route       string    `json:"route"` // "local": same subnet, "routed": through a router/VPN
	Metrics     []Metric  `json:"metrics"`
	Verdict     string    `json:"verdict"`
	Headline    string    `json:"headline"`
	Findings    []string  `json:"findings"` // flat list, read by older versions
	Notes       []Finding `json:"notes"`    // grouped by how much you will notice
}

// Finding: Level is "poor" (you will notice), "fair" (you may notice),
// "info" (good to know) or "good".
type Finding struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// autoProfile guesses what the connection is supposed to be, and why.
func (r *Result) autoProfile() (string, string) {
	ip := r.RemoteIP
	switch {
	case isTailscaleIP(parseIP(ip)):
		return "vpn", "the other computer was reached over Tailscale"
	case r.LocalKind == "vpn":
		return "vpn", "this computer used its VPN connection (" + r.LocalIface + ")"
	case r.Route == "routed" && r.IdleRTT > 3:
		return "vpn", fmt.Sprintf("the other computer is on a different network (%s) and delay is %s, so traffic goes through a router, VPN or the internet", ip, ms(r.IdleRTT))
	case r.Route == "routed":
		return "1g", fmt.Sprintf("the other computer is on a different subnet (%s) but delay is LAN-like, so this looks like a routed LAN", ip)
	case r.IdleRTT > 5 && r.LocalKind != "wifi" && r.RemoteKind != "wifi":
		return "vpn", fmt.Sprintf("delay is %s, far higher than any local network, so traffic must be leaving the building", ms(r.IdleRTT))
	case r.LocalKind == "wifi" || r.RemoteKind == "wifi":
		return "wifi", "one of the computers is on Wi-Fi"
	}
	link := minNonZero(r.LocalLinkMbps, r.RemoteLinkMbps)
	fast := r.LocalLinkMbps
	if r.RemoteLinkMbps > fast {
		fast = r.RemoteLinkMbps
	}
	if link > 0 && link < 1000 && fast >= 1000 {
		// One end is gigabit, so the network is meant to be: the slow end is the fault.
		return "1g", "one computer is connected at " + fmtLink(fast) + ", so this network should be gigabit, but the other end is only at " + fmtLink(link)
	}
	if link > 0 && link < 1000 {
		return "100m", "both computers are connected at " + fmtLink(link)
	}
	if link >= 1000 {
		return "1g", "both computers are on the same network, wired at " + fmtLink(link) + " or faster"
	}
	return "1g", "both computers are on the same network (link speed unknown, assuming gigabit)"
}

func minNonZero(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}

func grade3(v, good, fair float64, higherBetter bool) string {
	if higherBetter {
		switch {
		case v >= good:
			return "good"
		case v >= fair:
			return "fair"
		}
		return "poor"
	}
	switch {
	case v <= good:
		return "good"
	case v <= fair:
		return "fair"
	}
	return "poor"
}

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
func ms(v float64) string {
	if v < 10 {
		return f2(v) + " ms"
	}
	return f1(v) + " ms"
}
func itoa(i int) string            { return strconv.Itoa(i) }
func ftoa(v float64, p int) string { return strconv.FormatFloat(v, 'f', p, 64) }
func mbps(v float64) string {
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64) + " Mb/s"
	}
	return f1(v) + " Mb/s"
}

// Grade scores the result and writes the plain English verdict. Only things a
// person would actually hear or feel can make the verdict worse.
func (r *Result) Grade(profile string) {
	r.ProfileAuto = profile == "" || profile == "auto"
	r.AutoReason = ""
	if r.ProfileAuto {
		profile, r.AutoReason = r.autoProfile()
	}
	p := profiles[profile]
	r.ProfileKey, r.ProfileName = p.Key, p.Name
	r.Metrics, r.Findings, r.Notes = nil, nil, nil
	if r.Practice {
		r.gradePractice()
		return
	}
	lan := p.Key == "1g" || p.Key == "100m"

	thrExpect := "about " + mbps(p.ExpectMbps) + " on a healthy link"
	if p.ExpectMbps == 0 {
		thrExpect = "noticeably slow below " + mbps(p.ThrGood)
	}
	bloat := maxf(0, maxf(r.DownLoadedRTT, r.UpLoadedRTT)-r.IdleRTT)
	bf, bp := float64(bloatFair), float64(bloatPoor)
	if lan {
		// A LAN is only this full during a flat-out file copy, so a queue
		// there matters less: flag it later and never as the main problem.
		bf, bp = bloatFairLAN, 1e9
	}
	bloatGrade := grade3(bloat, bf, bp, false)
	thrGrade := func(v float64) string {
		if r.RateCapMbps > 0 && v >= r.RateCapMbps*0.8 {
			return "info" // as fast as the radio allowed: says nothing about the network
		}
		return grade3(v, p.ThrGood, p.ThrFair, true)
	}
	if r.RateCapMbps > 0 {
		thrExpect = "limited to " + mbps(r.RateCapMbps) + " by the radio"
	}
	r.Metrics = []Metric{
		{Key: "down", Label: "Speed  " + r.RemoteHost + " → " + r.LocalHost, Value: r.DownMbps, Unit: "Mb/s", Grade: thrGrade(r.DownMbps), Expect: thrExpect},
		{Key: "up", Label: "Speed  " + r.LocalHost + " → " + r.RemoteHost, Value: r.UpMbps, Unit: "Mb/s", Grade: thrGrade(r.UpMbps), Expect: thrExpect},
		{Key: "rtt", Label: "Delay (round trip)", Value: r.IdleRTT, Unit: "ms", Grade: grade3(r.IdleRTT, rttFair, rttPoor, false), Expect: "felt above " + itoa(rttFair) + " ms"},
		{Key: "jitter", Label: "Jitter", Value: r.JitterMs, Unit: "ms", Grade: grade3(r.JitterMs, jitFair, jitPoor, false), Expect: "heard above " + itoa(jitFair) + " ms"},
		{Key: "bloat", Label: "Extra delay when busy", Value: bloat, Unit: "ms", Grade: bloatGrade, Expect: "felt above " + itoa(int(bf)) + " ms"},
	}
	if loss, jit, ok := r.udpWorst(); ok {
		r.Metrics = append(r.Metrics,
			Metric{Key: "udploss", Label: "Stream loss (" + f1(r.UDPRateMbps) + " Mb/s UDP)", Value: loss, Unit: "%", Grade: grade3(loss, lossFair, lossPoor, false), Expect: "heard above " + f2(lossFair) + " %"},
			Metric{Key: "udpjitter", Label: "Stream jitter", Value: jit, Unit: "ms", Grade: grade3(jit, jitFair, jitPoor, false), Expect: "heard above " + itoa(jitFair) + " ms"})
	} else if r.LatencyMethod == "UDP" {
		// Only the 300 probes: too few to grade small loss, so count packets.
		g := "good"
		switch {
		case r.Lost >= 6:
			g = "poor"
		case r.Lost >= 2:
			g = "fair"
		}
		r.Metrics = append(r.Metrics, Metric{Key: "loss", Label: "Packet loss", Value: r.LossPct, Unit: "%", Grade: g, Expect: "0 %"})
	}
	if m := r.MTU; m != nil && m.DFSupported && m.PathMTU > 0 {
		g := "good"
		if m.PathMTU < 1280 || (m.FragTested && !m.FragmentsOK) {
			g = "fair"
		}
		r.Metrics = append(r.Metrics, Metric{Key: "mtu", Label: "Largest packet (MTU)", Value: float64(m.PathMTU), Unit: "bytes", Grade: g, Expect: "1500 normal, 1280+ fine"})
	}

	// ---- findings, in the words of what you will experience
	add := func(level, text string) { r.Notes = append(r.Notes, Finding{level, text}) }
	sides := []struct {
		host, iface, kind, duplex string
		link                      int
	}{
		{r.LocalHost, r.LocalIface, r.LocalKind, r.LocalDuplex, r.LocalLinkMbps},
		{r.RemoteHost, r.RemoteIface, r.RemoteKind, r.RemoteDuplex, r.RemoteLinkMbps},
	}
	fastest := maxf(float64(r.LocalLinkMbps), float64(r.RemoteLinkMbps))
	// When one end is stuck at 100 Mb/s, a speed of ~80-95 Mb/s is simply that
	// limit: report it once, as the adapter problem, at the speed's level.
	linkCapped := false
	capLevel := "fair"
	for _, sd := range sides {
		if sd.kind == "wired" && sd.link == 100 && (fastest >= 1000 || p.Key == "1g") {
			linkCapped = true
		}
	}
	if linkCapped {
		for _, m := range r.Metrics[:2] {
			if m.Value >= 75 && m.Value <= 100 && m.Grade == "poor" {
				capLevel = "poor"
			}
		}
	}
	for _, sd := range sides {
		switch {
		case sd.kind == "wired" && sd.link > 0 && sd.link <= 10:
			add("poor", fmt.Sprintf("Everything to and from %s will feel slow: its network adapter (%s) is connected at only %d Mb/s. That almost always means a broken cable or a damaged port.", sd.host, sd.iface, sd.link))
		case sd.kind == "wired" && sd.link == 100 && (fastest >= 1000 || p.Key == "1g"):
			add(capLevel, fmt.Sprintf("Large file copies to and from %s will be about 10 times slower than they should: its network adapter (%s) is connected at 100 Mb/s instead of 1 Gb/s. Radio audio and remote control are not affected. Usually a bad or cheap cable, a wall jack, or an old switch.", sd.host, sd.iface))
		case sd.kind == "wired" && sd.link == 100:
			add("info", fmt.Sprintf("%s is connected at 100 Mb/s. Plenty for radio and remote control; large file copies are limited to about 11 MB per second.", sd.host))
		}
		if sd.duplex == "half" && sd.kind == "wired" {
			add("poor", fmt.Sprintf("Expect slow, unreliable transfers to and from %s: its adapter runs in half duplex, which causes collisions and lost packets. Set the adapter and the switch port to Auto.", sd.host))
		}
	}

	for _, m := range r.Metrics[:2] {
		if m.Grade != "fair" && m.Grade != "poor" {
			continue
		}
		if linkCapped && m.Value >= 75 && m.Value <= 100 {
			continue // already explained by the 100 Mb/s adapter
		}
		dir := r.RemoteHost + " to " + r.LocalHost
		if m.Key == "up" {
			dir = r.LocalHost + " to " + r.RemoteHost
		}
		var s string
		switch {
		case p.Key == "vpn" && m.Grade == "poor":
			s = fmt.Sprintf("Remote audio plus a panadapter or video may not fit: from %s the link only carried %s.", dir, mbps(m.Value))
		case p.Key == "vpn":
			s = fmt.Sprintf("Remote audio will work, but panadapter, video or file transfers may struggle: from %s the link carried %s.", dir, mbps(m.Value))
		case p.ExpectMbps > 0:
			s = fmt.Sprintf("Copying files from %s takes about %d times longer than it should: it moved %s, where a healthy %s does about %s.", dir, int(p.ExpectMbps/maxf(m.Value, 1)+0.5), mbps(m.Value), p.Name, mbps(p.ExpectMbps))
		default:
			s = fmt.Sprintf("Large transfers from %s will feel slow: %s, where good %s does %s or more.", dir, mbps(m.Value), p.Name, mbps(p.ThrGood))
		}
		add(m.Grade, s)
	}
	if g := gradeOf(r.Metrics, "rtt"); g == "fair" || g == "poor" {
		add(g, fmt.Sprintf("Remote operation will feel laggy: every action takes %s to reach the other end and come back. Above about %d ms people notice it in remote control, and CW and push-to-talk timing suffer.", ms(r.IdleRTT), rttFair))
	}
	jit := r.JitterMs
	if _, sj, ok := r.udpWorst(); ok {
		jit = maxf(jit, sj)
	}
	if g := grade3(jit, jitFair, jitPoor, false); g != "good" {
		add(g, fmt.Sprintf("Live audio may stutter: the delay keeps changing by about %s. Raising the audio or network buffer in your radio software hides this, at the cost of a little more delay.", ms(jit)))
	}
	for _, d := range []struct {
		st       *UDPStats
		from, to string
	}{{r.UDPDown, r.RemoteHost, r.LocalHost}, {r.UDPUp, r.LocalHost, r.RemoteHost}} {
		if d.st == nil || d.st.Sent <= d.st.Received {
			continue
		}
		g := grade3(d.st.LossPct, lossFair, lossPoor, false)
		if g == "good" {
			g = "info"
		}
		lost := d.st.Sent - d.st.Received
		what := "You will hear it: expect clicks, dropouts and gaps in the panadapter"
		if g == "fair" {
			what = "You may hear it now and then as a click or a short gap in the audio"
		} else if g == "info" {
			what = "Too few to hear"
		}
		add(g, fmt.Sprintf("%s. %d of %d packets of a steady %s Mb/s stream from %s to %s were lost (%s%%), and real-time audio and SDR data cannot be resent.", what, lost, d.st.Sent, f1(r.UDPRateMbps), d.from, d.to, f2(d.st.LossPct)))
	}
	if g := gradeOf(r.Metrics, "loss"); g == "fair" || g == "poor" {
		add(g, fmt.Sprintf("Expect occasional dropouts: %d of %d test packets were lost.", r.Lost, r.Sent))
	}
	if g := gradeOf(r.Metrics, "bloat"); g == "fair" || g == "poor" {
		busy := maxf(r.DownLoadedRTT, r.UpLoadedRTT)
		where := "while someone downloads or uploads something big"
		if lan {
			where = "while a large file copy is running between computers"
		}
		add(g, fmt.Sprintf("Everything gets laggy %s: delay went from %s to %s when the link was full (\"bufferbloat\"). Remote control and audio will lag at those times.", where, ms(r.IdleRTT), ms(busy)))
	} else if bloat > 10 {
		add("info", fmt.Sprintf("When the link was completely full (like a big file copy), delay rose by %s. You are unlikely to notice.", ms(bloat)))
	}
	if r.LatencyMethod != "UDP" || r.UDPNote != "" {
		msg := r.UDPNote
		if msg == "" {
			msg = "UDP packets are blocked between these computers (probably a firewall)."
		}
		add("poor", "Ham radio audio, SDR data and most real-time apps use UDP, and it does not get through here. "+msg)
	}
	if m := r.MTU; m != nil && m.PathMTU > 0 {
		switch {
		case m.PathMTU < 1280:
			add("fair", fmt.Sprintf("Some VPN and real-time apps may fail or disconnect: the largest packet that gets through is only %d bytes.", m.PathMTU))
		case m.PathMTU < 1500:
			add("info", fmt.Sprintf("Packets are limited to %d bytes on this path (normal is 1500). Usual on VPNs and PPPoE lines and normally unnoticeable; apps that send large UDP packets should stay below %d bytes.", m.PathMTU, m.PathMTU-28))
		}
		if m.FragTested && !m.FragmentsOK {
			add("fair", "Apps that send large UDP packets will fail silently here: packets too big for the path are dropped instead of being split.")
		}
	}
	for _, d := range []*UDPStats{r.UDPDown, r.UDPUp} {
		if d != nil && d.Received > 0 && float64(d.OutOfOrder)/float64(d.Received) > 0.001 {
			add("info", fmt.Sprintf("%d stream packets arrived out of order. Some apps treat these as lost. Common on VPNs that use several paths.", d.OutOfOrder))
			break
		}
	}
	if r.Route == "routed" && lan && !r.ProfileAuto && r.IdleRTT > 3 {
		add("info", fmt.Sprintf("The other computer is on a different network (%s) with %s delay. If this goes through a VPN or the internet, choose \"VPN / remote site / Internet\" for a fair comparison.", r.RemoteIP, ms(r.IdleRTT)))
	}
	for _, v := range r.VersionNotes {
		add("info", v)
	}
	if len(r.Skipped) > 0 {
		var names []string
		for _, k := range r.Skipped {
			names = append(names, featureNames[k])
		}
		add("info", fmt.Sprintf("Not tested: %s, because %s runs Radio Bandwidth %s, which does not have it. Update it to get the full test.", strings.Join(names, " and "), r.RemoteHost, r.RemoteVersion))
	}
	if r.RateCapMbps > 0 {
		add("info", fmt.Sprintf("%s limits test traffic to %s so a test cannot disturb live operation. Speed here shows only whether the link can carry that much.", r.RemoteHost, mbps(r.RateCapMbps)))
	}
	if p.Key == "vpn" {
		add("info", "This test went through a VPN or the internet, so speed depends on both internet connections. What matters most here is delay, jitter and loss.")
		if isTailscaleIP(parseIP(r.RemoteIP)) && r.IdleRTT > 40 {
			add("info", "If Tailscale is relaying this connection (\"via DERP\" in tailscale ping), delay will be higher than a direct connection.")
		}
	}

	// ---- verdict: only what you would notice counts
	worst := "good"
	for _, f := range r.Notes {
		if f.Level == "poor" || (f.Level == "fair" && worst == "good") {
			worst = f.Level
		}
	}
	for _, m := range r.Metrics {
		if m.Grade == "poor" || (m.Grade == "fair" && worst == "good") {
			worst = m.Grade
		}
	}
	rank := map[string]int{"poor": 0, "fair": 1, "info": 2, "good": 3}
	sort.SliceStable(r.Notes, func(i, j int) bool { return rank[r.Notes[i].Level] < rank[r.Notes[j].Level] })
	r.Verdict = worst
	switch worst {
	case "good":
		r.Headline = "No problems you would notice"
		r.Notes = append([]Finding{{"good", "Radio audio, remote control and file copies should all work smoothly over this connection."}}, r.Notes...)
	case "fair":
		r.Headline = "Works, but you may notice something"
	default:
		r.Headline = "You will notice problems on this connection"
	}
	for _, f := range r.Notes {
		r.Findings = append(r.Findings, f.Text)
	}
	r.Fixes = buildFixes(r, p)
}

// gradePractice: a loopback test never touches the network, so the numbers
// only show how Radio Bandwidth works. Nothing is graded.
func (r *Result) gradePractice() {
	r.ProfileKey, r.ProfileName, r.ProfileAuto, r.AutoReason = "practice", "Practice run (no network)", true, ""
	exp := "practice run: not graded"
	r.Metrics = []Metric{
		{Key: "down", Label: "Download (practice)", Value: r.DownMbps, Unit: "Mb/s", Grade: "info", Expect: exp},
		{Key: "up", Label: "Upload (practice)", Value: r.UpMbps, Unit: "Mb/s", Grade: "info", Expect: exp},
		{Key: "rtt", Label: "Delay (round trip)", Value: r.IdleRTT, Unit: "ms", Grade: "info", Expect: exp},
		{Key: "jitter", Label: "Jitter", Value: r.JitterMs, Unit: "ms", Grade: "info", Expect: exp},
		{Key: "loss", Label: "Packet loss", Value: r.LossPct, Unit: "%", Grade: "info", Expect: exp},
		{Key: "bloat", Label: "Extra delay when busy", Value: maxf(0, maxf(r.DownLoadedRTT, r.UpLoadedRTT)-r.IdleRTT), Unit: "ms", Grade: "info", Expect: exp},
	}
	if loss, jit, ok := r.udpWorst(); ok {
		r.Metrics = append(r.Metrics,
			Metric{Key: "udploss", Label: "UDP stream loss (" + f1(r.UDPRateMbps) + " Mb/s)", Value: loss, Unit: "%", Grade: "info", Expect: exp},
			Metric{Key: "udpjitter", Label: "UDP stream jitter", Value: jit, Unit: "ms", Grade: "info", Expect: exp})
	}
	r.Fixes = nil
	r.Verdict = "practice"
	r.Headline = "Practice run finished"
	r.Notes = []Finding{
		{"info", "This test never left this computer, so the speeds are far higher than any real network and nothing is graded. It only shows you what a test looks like."},
		{"info", "For a real test, start Radio Bandwidth on a second computer and pick it from the list (or type its address)."},
	}
	r.Findings = nil
	for _, f := range r.Notes {
		r.Findings = append(r.Findings, f.Text)
	}
}

// udpWorst returns the worse direction's UDP stream loss and jitter.
func (r *Result) udpWorst() (float64, float64, bool) {
	loss, jit, ok := 0.0, 0.0, false
	for _, s := range []*UDPStats{r.UDPDown, r.UDPUp} {
		if s != nil {
			loss, jit, ok = maxf(loss, s.LossPct), maxf(jit, s.JitterMs), true
		}
	}
	return loss, jit, ok
}

func gradeOf(ms []Metric, key string) string {
	for _, m := range ms {
		if m.Key == key {
			return m.Grade
		}
	}
	return "info"
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Text is a plain text report for the console or for pasting into an email.
func (r *Result) Text() string {
	var b strings.Builder
	mark := map[string]string{"good": "OK  ", "fair": "MAY ", "poor": "BAD ", "info": "    "}
	fmt.Fprintf(&b, "Radio Bandwidth %s report  %s\n", r.Version, r.Time)
	remote := r.RemoteIP
	if r.RemoteName != "" {
		remote = r.RemoteName + " = " + r.RemoteIP
	}
	fmt.Fprintf(&b, "%s (%s) <-> %s (%s)\n", r.LocalHost, nz(r.LocalIP), r.RemoteHost, remote)
	fmt.Fprintf(&b, "This end:  %s\n", sideDesc(r.LocalIface, r.LocalKind, r.LocalLinkMbps))
	fmt.Fprintf(&b, "Far end:   %s\n", sideDesc(r.RemoteIface, r.RemoteKind, r.RemoteLinkMbps))
	if r.RemoteVersion != "" && r.RemoteVersion != r.Version {
		fmt.Fprintf(&b, "Versions:  this end %s, far end %s\n", r.Version, r.RemoteVersion)
	}
	auto := ""
	if r.ProfileAuto && r.AutoReason != "" {
		auto = " (auto: " + r.AutoReason + ")"
	} else if !r.ProfileAuto {
		auto = " (chosen by user)"
	}
	fmt.Fprintf(&b, "Compared against: %s%s\n\n", r.ProfileName, auto)
	fmt.Fprintf(&b, ">>> %s <<<\n\n", strings.ToUpper(r.Headline))
	for _, m := range r.Metrics {
		v := f2(m.Value) + " " + m.Unit
		switch m.Unit {
		case "Mb/s":
			v = mbps(m.Value)
		case "bytes":
			v = itoa(int(m.Value)) + " bytes"
		}
		fmt.Fprintf(&b, "  [%s] %-34s %14s   %s\n", mark[m.Grade], m.Label, v, m.Expect)
	}
	fmt.Fprintf(&b, "\n  Delay range %s to %s, %d probes over %s, busy delay %s down / %s up\n",
		ms(r.IdleRTTMin), ms(r.IdleRTTMax), r.Sent, r.LatencyMethod, ms(r.DownLoadedRTT), ms(r.UpLoadedRTT))
	for _, d := range []struct {
		name string
		st   *UDPStats
	}{{"UDP stream " + r.RemoteHost + " -> " + r.LocalHost, r.UDPDown}, {"UDP stream " + r.LocalHost + " -> " + r.RemoteHost, r.UDPUp}} {
		if d.st != nil {
			fmt.Fprintf(&b, "  %s at %s Mb/s: %d/%d received, %s%% lost, jitter %s, %d out of order, delivered %s\n",
				d.name, f1(r.UDPRateMbps), d.st.Received, d.st.Sent, f2(d.st.LossPct), ms(d.st.JitterMs), d.st.OutOfOrder, mbps(d.st.Mbps))
		}
	}
	b.WriteString("\n")
	if m := r.MTU; m != nil && m.PathMTU > 0 {
		fmt.Fprintf(&b, "  Path MTU %d bytes, fragmented UDP %s\n\n", m.PathMTU, map[bool]string{true: "passes", false: "is DROPPED"}[m.FragmentsOK])
	}
	heads := map[string]string{"poor": "YOU WILL NOTICE", "fair": "YOU MAY NOTICE", "info": "GOOD TO KNOW", "good": ""}
	last := "-"
	for _, f := range r.Notes {
		if f.Level != last && heads[f.Level] != "" {
			fmt.Fprintf(&b, "%s\n", heads[f.Level])
		}
		last = f.Level
		fmt.Fprintf(&b, "  * %s\n", f.Text)
	}
	if len(r.Notes) == 0 {
		for _, f := range r.Findings {
			fmt.Fprintf(&b, "  * %s\n", f)
		}
	}
	if len(r.Fixes) > 0 {
		b.WriteString("\nHOW TO FIX IT\n")
		for i, f := range r.Fixes {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, f.Title)
			for _, s := range f.Steps {
				fmt.Fprintf(&b, "   - %s\n", s)
			}
		}
	}
	return b.String()
}

func nz(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}

func sideDesc(iface, kind string, link int) string {
	if iface == "" {
		return "unknown connection"
	}
	k := map[string]string{"wired": "wired", "wifi": "Wi-Fi", "vpn": "VPN", "virtual": "virtual"}[kind]
	s := iface + " (" + k
	if link > 0 && kind != "vpn" {
		s += ", link " + fmtLink(link)
	}
	return s + ")"
}
