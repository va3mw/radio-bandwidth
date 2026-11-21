package main

import (
	"fmt"
	"net"
)

// Fix is a plain language "how to fix it" checklist for one problem.
type Fix struct {
	Title string   `json:"title"`
	Steps []string `json:"steps"`
}

// buildFixes turns the measured problems into practical steps, most
// important first. Grading must already have run.
func buildFixes(r *Result, p Profile) []Fix {
	var out []Fix
	add := func(f Fix) { out = append(out, f) }
	bad := func(key string) bool { g := gradeOf(r.Metrics, key); return g == "fair" || g == "poor" }
	lan := p.Key == "1g" || p.Key == "100m"

	// --- physical layer first: nothing else matters until this is right
	slowSide := ""
	fastest := r.LocalLinkMbps
	if r.RemoteLinkMbps > fastest {
		fastest = r.RemoteLinkMbps
	}
	for _, s := range []struct {
		host, kind string
		link       int
	}{{r.LocalHost, r.LocalKind, r.LocalLinkMbps}, {r.RemoteHost, r.RemoteKind, r.RemoteLinkMbps}} {
		if s.kind == "wired" && s.link > 0 && (s.link <= 10 || (s.link == 100 && (fastest >= 1000 || p.Key == "1g"))) {
			slowSide = s.host
		}
	}
	if slowSide != "" {
		add(Fix{"Get " + slowSide + " connected at 1 Gb/s", []string{
			"Swap the network cable for a known good one, Cat5e or Cat6. A cable with a damaged wire still works, but only at 100 Mb/s.",
			"Look at the lights on the switch port: on most switches a different colour (often amber instead of green) means 100 Mb/s.",
			"Try a different port on the switch, and remove any wall jack, coupler or old 10/100 switch in between to find the weak link.",
			"In the network adapter settings, set Speed & Duplex to \"Auto Negotiation\" (never a fixed speed), then unplug and replug the cable.",
			"Cheap powerline (network over mains) adapters and old 10/100 switches cap the speed: replace them with gigabit equipment.",
		}})
	}
	if r.LocalDuplex == "half" || r.RemoteDuplex == "half" {
		add(Fix{"Fix the duplex setting", []string{
			"Set both the network adapter and the switch port to Auto. A fixed setting on only one end causes a duplex mismatch, the classic cause of slow, lossy wired links.",
			"If the switch is managed, check the port's error counters (CRC, late collisions) after the change; they should stop climbing.",
		}})
	}

	// --- packet loss on a wired LAN is never normal
	lanLoss := lan && (bad("loss") || bad("udploss"))
	if lanLoss {
		add(Fix{"Find where packets are being lost", []string{
			"Replace the patch cables at both ends first; loss on a wired LAN is almost always a bad cable, connector or wall jack.",
			"Keep data cables away from power cables, fluorescent lights and motors (and away from transmitting antennas and feed lines: RF getting into Ethernet causes exactly this).",
			"Run the test again with both computers plugged into the same switch. If the loss disappears, the problem is in the cable run or the switches in between.",
			"Restart the switches and router. If loss returns after a while, a switch may be overheating or failing.",
			"On a managed switch, look for ports with CRC or input errors.",
		}})
	}

	// --- slow LAN with a good link
	if lan && slowSide == "" && (bad("down") || bad("up")) {
		steps := []string{
			"Make sure nothing else is using the network heavily during the test (backups, downloads, video streaming, cloud sync).",
			"Check that both computers really are wired: a laptop on Wi-Fi and Ethernet at once may use the Wi-Fi.",
			"Plug both computers into the same switch and test again. If it is fast there, a switch or link in between is the bottleneck (often an old 100 Mb/s switch hidden in a cupboard or a powerline adapter).",
			"Update the network adapter driver, and turn off \"Energy Efficient Ethernet\" / \"Green Ethernet\" in the adapter's advanced settings.",
			"Temporarily disable antivirus web/network scanning and VPN clients on both computers to see whether they are slowing traffic.",
		}
		if r.DownMbps > 0 && r.UpMbps > 0 && minf(r.DownMbps, r.UpMbps) < maxf(r.DownMbps, r.UpMbps)*0.6 {
			steps = append([]string{"Speed is very different in each direction. Swap the cable and switch port of the slower sender first; a damaged pair or a duplex problem often affects one direction only."}, steps...)
		}
		add(Fix{"Get the full speed out of the LAN", steps})
	}

	// --- Wi-Fi
	if p.Key == "wifi" && (bad("down") || bad("up") || bad("loss") || bad("udploss") || bad("jitter") || bad("udpjitter")) {
		add(Fix{"Use a cable, or make the Wi-Fi steadier", []string{
			"For the radio or SDR computer, use a network cable: it is the only fix that removes the problem. Wi-Fi takes turns on a shared channel, so delay and loss come and go however good the signal is. If you cannot run a cable, MoCA adapters over existing TV coax come close to a cable; powerline adapters are hit and miss.",
			"Turn off Wi-Fi power saving on the computer, which makes the adapter doze between packets. On Windows: Control Panel > Power Options > Change plan settings > Change advanced power settings > Wireless Adapter Settings > Power Saving Mode > Maximum Performance, and keep a laptop plugged in. In the adapter's Advanced settings (Device Manager), set \"MIMO Power Save Mode\" to \"No SMPS\" if it is there. On Linux: sudo iw dev <wifi-name> set power_save off.",
			"Stop the computer looking for other networks while you operate. Windows scans now and then, often about once a minute, and each scan pauses traffic: that shows as regular lag spikes and audio clicks. Close the Wi-Fi network list, and for a computer that stays put, set \"Roaming Aggressiveness\" to \"Lowest\" and, on Intel adapters, \"Global BG Scan Blocking\" to \"Always\" in the adapter's Advanced settings.",
			"Update the Wi-Fi driver from the adapter maker (Intel, Realtek, MediaTek, Qualcomm), not only Windows Update. Many lag-spike problems are driver bugs.",
			"Find out what else is on the Wi-Fi. Every device on the same channel takes turns with yours, so a TV streaming, a cloud backup, phones uploading photos or one old slow device makes everyone wait. Move busy devices to a cable or to the other band.",
			"Give the 5 GHz (or 6 GHz) band its own network name and connect the radio computer only to that, so it cannot hop between bands or mesh units in the middle of a session.",
			"In the router or access point: make sure WMM is on (Wi-Fi traffic priority needs it), choose a fixed channel instead of Auto, avoid DFS channels (52 to 144 on 5 GHz: when the router detects radar it must stop and move, which drops everyone for a minute or more), and use 80 MHz channel width rather than 160 MHz.",
			"With a mesh system, connect the mesh units to each other with cables (wired backhaul). Every hop over the air adds delay and jitter and cuts the speed.",
			"Moving closer to the access point only helps when the signal is weak (one or two bars, or worse than -67 dBm). With a good signal it changes nothing, and the steps above matter more.",
		}})
	}

	// --- jitter and bufferbloat
	if bad("bloat") && lan {
		add(Fix{"Stop the LAN getting laggy during big copies", []string{
			"This only happens while the network is completely full, for example during a large file copy or backup between computers. Schedule those for when you are not operating.",
			"In the network adapter's advanced settings on both computers, try turning \"Interrupt Moderation\" off and \"Flow Control\" on, then test again.",
			"Cheap unmanaged switches queue a lot of traffic. A better switch, or a managed switch with QoS giving your radio traffic priority, keeps it responsive.",
		}})
	} else if bad("bloat") {
		add(Fix{"Stop the connection getting laggy when it is busy (bufferbloat)", []string{
			"In the router, turn on SQM / Smart Queue Management, \"cake\" or \"fq_codel\" (OpenWrt, pfSense, OPNsense, Ubiquiti and many others have it), and set it to about 90% of your real line speed.",
			"If the router has no such setting, QoS that limits big downloads and uploads helps; turning it off on a router whose QoS is badly done can also help.",
			"Schedule backups and large cloud sync for when you are not operating.",
		}})
	}
	if bad("jitter") || bad("udpjitter") {
		steps := []string{
			"Fix any packet loss and bufferbloat first; they are the usual cause of jitter.",
			"Use a cable instead of Wi-Fi for anything real-time.",
			"In your radio or audio software, increase the network or audio buffer a little. More buffer means a bit more delay but no dropouts.",
		}
		if p.Key == "vpn" {
			steps = append(steps, "Over the internet, jitter often comes from the slowest upload link. Ask your provider about line quality, or test at a quieter time of day to compare.")
		}
		add(Fix{"Reduce jitter", steps})
	}

	// --- VPN specific
	if p.Key == "vpn" && (bad("rtt") || bad("loss") || bad("udploss")) {
		steps := []string{
			"Test each site's internet connection on its own (a normal speed test at both ends). The VPN can never be faster or cleaner than the worse of the two.",
			"Make sure both ends connect directly rather than through a relay server.",
			"Check the VPN endpoint (router or PC) is not overloaded: encryption on a small router can be the bottleneck.",
		}
		if ip := net.ParseIP(r.RemoteIP); isTailscaleIP(ip) {
			steps = append(steps, "For Tailscale, run \"tailscale ping "+r.RemoteHost+"\": if it says \"via DERP\" the traffic is relayed. Allowing UDP port 41641 through both firewalls, or enabling UPnP / NAT-PMP on the routers, usually makes it direct.")
		}
		add(Fix{"Make the VPN path cleaner", steps})
	}

	// --- MTU and fragmentation
	if m := r.MTU; m != nil && m.PathMTU > 0 {
		if m.PathMTU < 1280 {
			add(Fix{fmt.Sprintf("Fix the packet size limit (MTU %d)", m.PathMTU), []string{
				"Find the device that lowers the MTU: a VPN or tunnel, a PPPoE internet connection (1492), or a switch/router with a wrong MTU setting.",
				"On a plain LAN every device should be at 1500. Check router and switch interface settings for a lower value.",
				"For VPNs, set the tunnel MTU to the measured value and enable MSS clamping on the VPN routers, so applications never send packets that are too big.",
				"Apps that send large UDP packets (some SDR and video streams) must be set to a packet size below " + fmt.Sprint(m.PathMTU-28) + " bytes, or they will fail on this path.",
			}})
		}
	}
	if m := r.MTU; m != nil && m.FragTested && !m.FragmentsOK && m.PathMTU > 0 {
		add(Fix{"Let fragmented packets through", []string{
			"A firewall, router or VPN on this path drops UDP packets that are too big and have to be split. Small packets work, so problems look random.",
			"In the firewall, allow IP fragments (some firewalls have a \"drop fragmented packets\" or \"scrub\" option).",
			"Otherwise make sure no application sends UDP packets larger than the path MTU (see the MTU result).",
		}})
	}

	// --- UDP blocked
	if r.LatencyMethod != "UDP" || r.UDPNote != "" {
		add(Fix{"Allow UDP between the computers", []string{
			"Ham radio audio, SDR data and most real-time apps use UDP, so this has to work.",
			"On Windows: Windows Security > Firewall & network protection > Allow an app through firewall > tick Radio Bandwidth for Private and Public (and the same for your radio software).",
			"On Debian with ufw: sudo ufw allow 47830:47831/udp",
			"Check the router or VPN firewall rules between the two networks allow UDP, not just TCP.",
		}})
	}
	return out
}
