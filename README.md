# Radio Bandwidth

Is the network between your computer and your FlexRadio good enough for SmartSDR, and if not,
why not?

Radio Bandwidth has two parts:

- **A waveform for the radio** (FLEX-8000 series and Aurora), installed with SmartSDR's Waveform
  Manager. It answers bandwidth tests and makes the radio easy to find.
- **An app for Windows and macOS.** It finds your radio, tests the path to it with traffic shaped
  like real SmartSDR streams, and tells you in plain language whether SmartSDR will work well,
  for each way you use it (basic operation, several panadapters, DAX audio, DAX IQ), and what to
  fix if not. One file, nothing to install.

MIT licensed. Built on [NetProof](https://github.com/va3mw/netproof).

## Status

Early development. The code is NetProof's, renamed: it already tests between computers and
radios, but the radio-specific test is not built yet. The rest of this README describes the
current, inherited behaviour and will change as the milestones land.

| Milestone | What | State |
|---|---|---|
| M0 | Own product: name, ports (47830, 47831, 47883), protocol markers, signing key, MIT licence | done |
| M1 | Sample real SmartSDR traffic from Wireshark captures; build test profiles from it | next |
| M2 | Customer app tests radios only, never listens for tests | |
| M3 | Find radios with FlexRadio's own discovery broadcast; show radios without the waveform | |
| M4 | Bandwidth test per SmartSDR use case, with a gentle step-up to find the headroom | |
| M5 | Packaging and signed releases: Windows exe, Mac app, radio waveform | |
| M6 | Test on a real radio, including impact while receiving and transmitting | |

Radio Bandwidth uses its own ports, protocol markers and release signing key, so it runs next to
NetProof without either one confusing or updating the other.

- [Quick start](#quick-start)
- [What it measures](#what-it-measures)
- [How results are judged](#how-results-are-judged)
- [Features](#features)
- [Versions and updates](#versions-and-updates)
- [Running inside a FlexRadio](#running-inside-a-flexradio)
- [Command line](#command-line)
- [Ports and security](#ports-and-security)
- [Known issues](#known-issues)
- [Building](#building)
- [Project layout](#project-layout)

## Quick start

1. Run Radio Bandwidth on **both** computers: `radio-bandwidth.exe` on Windows, `Radio Bandwidth.app` on a Mac
   (from `RadioBandwidth-mac.zip`), `radio-bandwidth-linux-amd64` on Debian. A browser page opens by itself.
2. On **one** of them, click **Test** next to the other computer's name. If it is not listed
   (VPN, Tailscale, another subnet), type its address or name and click **Start test**.
3. Wait about 45 seconds. Both screens show the verdict and, if needed, a "How to fix it"
   checklist.

There is no client or server to choose. Whichever computer presses Test drives the test; the
other one answers automatically.

**New users** can try it on one computer first: click **Practice run** (the computer tests
itself, nothing is graded), or start a second copy with `radio-bandwidth --second` (on Windows,
double-click `Radio Bandwidth second copy.bat` next to `radio-bandwidth.exe`) and test between the two windows.

**Windows:** the first time, Windows Firewall asks whether to allow Radio Bandwidth. Tick **both**
Private and Public and click **Allow**, or the other computer cannot reach this one.

**Mac:** unzip `RadioBandwidth-mac.zip` and drag `Radio Bandwidth.app` to Applications. It runs on Apple
Silicon and Intel Macs (macOS 11 or newer). There is no Dock icon: the browser page is
Radio Bandwidth's window, and its **Quit Radio Bandwidth** button stops it; opening the app again reopens the
page. The first time, macOS refuses to open it because it is not signed by Apple yet: open
**System Settings > Privacy & Security**, click **Open Anyway** next to the Radio Bandwidth message, and
confirm. Then allow Radio Bandwidth to **find devices on local networks** and to **accept incoming
connections** when asked. The zip includes these steps as a text file.

**Debian:** `chmod +x radio-bandwidth-linux-amd64` once. Without a desktop it just prints its addresses
and answers tests. If `ufw` is on:

```bash
sudo ufw allow 47830:47831/tcp
```

```bash
sudo ufw allow 47830:47831/udp
```

## What it measures

About 45 seconds in total.

| Phase | Time | How |
|---|---|---|
| Delay, jitter, loss | 6 s | 300 small UDP probes (falls back to TCP if UDP is blocked) |
| Packet size (MTU) | ~1 s | largest UDP packet that crosses in one piece (Don't Fragment, exact to the byte), and whether packets that must be split still arrive |
| Download | 10 s | 4 parallel TCP streams, far end to this end, first second ignored |
| Upload | 10 s | the same, other direction, measured at the receiving end |
| Busy delay | during both | 10 probes a second while the link is full (bufferbloat) |
| UDP stream in | 8 s | a steady stream of 1200 byte UDP packets at a fixed rate, like radio audio, panadapter and IQ data: loss, one-way jitter, out-of-order packets, delivered rate |
| UDP stream out | 8 s | the same, other direction |

The UDP stream runs at 100 Mb/s on a gigabit LAN, 30 Mb/s on 100 Mb/s or Wi-Fi and 10 Mb/s over
a VPN, never more than 70% of the TCP speed just measured, so a slow link is tested for loss
rather than simply overrun.

## How results are judged

Radio Bandwidth only calls something out when a person would actually hear or feel it. Findings are
grouped as **You will notice** (the only thing that makes the verdict "problems"), **You may
notice** and **Good to know**. Each says what you will experience first (clicks and dropouts,
laggy remote control, slow file copies) and the numbers second, and every problem you would
notice comes with a "How to fix it" checklist.

| | Healthy 100 Mb/s LAN | Healthy 1 Gb/s LAN | Healthy VPN | You may notice | You will notice |
|---|---|---|---|---|---|
| Speed each way | 90 to 95 Mb/s | 900 to 945 Mb/s | depends on both internet links | below 80 / 700 / 10 Mb/s | below 40 / 300 / 3 Mb/s |
| Delay (round trip) | < 1 ms | < 1 ms | 10 to 40 ms | > 50 ms | > 150 ms |
| Jitter | < 1 ms | < 1 ms | < 3 ms | > 5 ms | > 15 ms |
| Stream loss | 0 % | 0 % | 0 % | > 0.05 % | > 0.5 % |
| Extra delay when busy | < 20 ms | < 10 ms | < 30 ms | > 50 ms (LAN: > 100 ms) | > 150 ms (never on a LAN) |
| Largest packet (MTU) | 1500 | 1500 | 1280 to 1420 | < 1280 | |

A wired link never reaches its label speed: about 6% goes to Ethernet, IP and TCP headers. Only
speed depends on the kind of link; the other limits are the same everywhere, because audio
stutters the same whether the jitter came from Wi-Fi or a VPN.

**Which kind of link?** Leave "What kind of connection is this?" on Automatic, or pick one.
Automatic uses the adapters' type and link speed, Tailscale addresses, whether the other
computer is on the same subnet, and the delay. A site-to-site VPN between two wired PCs is
recognised as a VPN; a gigabit PC paired with one stuck at 100 Mb/s is judged as gigabit, with
the slow end named as the problem.

The "How to fix it" checklists cover adapters stuck at 100 Mb/s, duplex, packet loss on a wired
LAN (cables, RF pickup, switches), slow LANs, Wi-Fi, bufferbloat (LAN and internet advice
differ), jitter (including raising the radio software's buffer), VPN paths (relays, Tailscale
DERP), MTU and fragmentation (tunnel MTU, MSS clamping) and UDP blocked by firewalls. They are
also in the saved and copied text report.

## Features

- **Finds other computers by itself**: on the LAN by broadcast, and on Tailscale by asking
  Tailscale which peers are online and checking which run Radio Bandwidth. FlexRadios running the
  Radio Bandwidth waveform show up with a **Radio** tag.
- **Tested before**: the last 10 computers tested, or that tested this one, with their last
  result and a green dot when Radio Bandwidth is running there now. Saved in
  `%AppData%\Radio Bandwidth\recent.json` (Windows) or `~/.config/Radio Bandwidth/recent.json` (Linux).
- **Pick the network connection** when a computer has several (Ethernet, Wi-Fi, Tailscale,
  other VPNs).
- **Names and Tailscale MagicDNS**: type `boathouse2` or `boathouse2.tail1234.ts.net`. The name is
  resolved once, IPv4 first, so every part of the test takes the same path; if the computer's DNS
  does not know it, Radio Bandwidth asks Tailscale directly.
- **Both screens show the result**: the computer that was tested shows the same report.
- **Copy or save the report** as plain text, for email or a support ticket.

## Versions and updates

Two copies do not need to be the same version. Each tells the other which tests it supports, and
a test skips only what the far end cannot do, saying so in the report ("Not tested: the UDP
stream test, because OldPC runs Radio Bandwidth 1.1.0").

**The far end is updated automatically.** When a tester finds the other computer running an older
Radio Bandwidth (1.7.0 or newer), it tells it to update, without asking anyone there. The other computer
downloads the newest release from this repository's GitHub Releases, checks it, replaces its own
program file and restarts on the same ports; the test waits up to 90 seconds for it, then
continues. If anything goes wrong (no internet, no write access to its folder, antivirus), the
test runs with the version it has, and the report says why the update did not happen.

- Copies older than 1.7.0 cannot update themselves: install the new version there once.
- A FlexRadio waveform is never updated this way; the report says when a newer one is available
  for SmartSDR's Waveform Manager.
- This computer shows a banner with **Update now** when a newer release is out.

**Why this is safe:** a tester cannot send code, only say "update". The other computer installs
nothing unless the release manifest is signed with the Radio Bandwidth release key (the public half is
built into Radio Bandwidth, the private half is a GitHub Actions secret), the downloaded file matches the
SHA-256 in that manifest, and the release is newer than what it runs. A tampered or foreign
release is refused.

**Publishing a release:** set `appVersion` in `main.go`, regenerate `rsrc_windows_*.syso`, commit,
and push a tag:

```bash
git tag v1.7.0
```

```bash
git push origin v1.7.0
```

The Release workflow tests, builds every platform and the waveform, signs the manifest and
publishes the release. The repository must be public so copies of Radio Bandwidth can download it, and
must have the `RBW_SIGNING_KEY` secret (from `go run ./tools/release keygen`). Keep a backup
of that key: copies in the field only trust releases signed with it.

## Running inside a FlexRadio

FLEX-8000 series and Aurora radios can run Radio Bandwidth as a Docker waveform. The radio then answers
tests by itself and appears on the LAN as, for example, **FLEX-8600 Shack (VA3MW)**, so you
test the path between your PC and the radio itself. See **[WAVEFORM.md](WAVEFORM.md)** for
installing, testing and building it.

## Command line

```bash
radio-bandwidth 192.168.1.50
```

Runs a test without the browser page and prints the report.

| Option | Meaning |
|---|---|
| `--profile auto\|1g\|100m\|wifi\|vpn` | what kind of link to judge against |
| `--bind <ip>` | test from this local address (network connection) |
| `--no-browser` | do not open the browser page |
| `--second` | a second copy on the same computer, for practice (ports 47840 and 47893) |
| `--responder` | answer tests only, no page (servers, and inside a FlexRadio) |
| `--name <text>` | name to announce instead of the computer name |
| `--max-mbps <n>` | responder: testers keep their traffic under this many Mb/s |

## Ports and security

| Port | Use |
|---|---|
| TCP + UDP 47830 | tests |
| UDP 47831 | LAN discovery broadcast |
| TCP 127.0.0.1:47883 | the browser page, on this computer only |

The browser page only answers this computer, and refuses requests from other web sites. Test
traffic is only sent to an address that has just connected over TCP, so Radio Bandwidth cannot be used
to flood a third party. Test durations and rates are capped.

## Known issues

- **Windows Defender may flag radio-bandwidth.exe** as `Trojan:Win32/Bearfoos.A!ml` or similar, mostly
  on Windows 10. This is a machine-learning false positive common to new, unsigned programs.
  Radio Bandwidth avoids the usual triggers (the Windows build never starts other programs, no
  PowerShell, full version information), but only code signing fixes it for good. Until then:
  Windows Security > Virus & threat protection > Protection history > the Radio Bandwidth entry >
  Actions > Allow on device, and report each release at
  https://www.microsoft.com/wdsi/filesubmission ("Software developer", "Incorrectly detected").
- **macOS asks for "Open Anyway" the first time**, because Radio Bandwidth is not signed and notarized
  by Apple. That needs an Apple Developer ID (US$99 a year); signing and notarizing can then be
  added to the release workflow. The Mac version has not been tried on a real Mac yet.
- **Tailscale peer discovery through the Tailscale service on Windows** has not been tested on a
  real tailnet. Typing the peer's address or MagicDNS name always works.

## Building

Requires Go 1.22 or newer. No C compiler.

```bash
./build.sh
```

Builds `dist/radio-bandwidth.exe` (Windows x64), `dist/radio-bandwidth-windows-arm64.exe`,
`dist/radio-bandwidth-linux-amd64`, `dist/radio-bandwidth-linux-arm64`, and for macOS `dist/RadioBandwidth-mac.zip`
(Radio Bandwidth.app with one universal program for Apple Silicon and Intel, made by
`tools/mkmacapp` without any Apple tools) plus the separate Mac programs.

```bash
./build-waveform.sh
```

Builds the FlexRadio waveform; see [WAVEFORM.md](WAVEFORM.md).

```bash
go test ./...
```

Runs the tests, including the grading of real recorded results.

Windows version information and the icon are in `rsrc_windows_*.syso`. After changing the
version in `main.go`, regenerate them with
[go-winres](https://github.com/tc-hib/go-winres):

```bash
go-winres simply --arch amd64,arm64 --out rsrc --manifest cli --product-name "Radio Bandwidth" --file-description "Radio Bandwidth: network tester for FlexRadio" --product-version X.Y.Z.0 --file-version X.Y.Z.0 --original-filename radio-bandwidth.exe --copyright "Copyright 2026 va3mw" --icon winres/icon.png
```

## Project layout

| File | What it does |
|---|---|
| `main.go` | start-up, options, responder mode |
| `server.go` | answering tests, LAN discovery, peers |
| `client.go` | running a test: delay, TCP speed, latency under load |
| `udpstream.go` | the fixed-rate UDP stream test |
| `mtu.go`, `dontfrag_*.go` | packet size (MTU) and fragmentation test |
| `grade.go` | judging results and writing the findings |
| `advice.go` | the "How to fix it" checklists |
| `iface.go`, `linkinfo_*.go` | network connections, link speed, Wi-Fi and VPN detection (macOS: `networksetup` and `ifconfig`) |
| `tailscale*.go` | Tailscale peers and MagicDNS names |
| `radio.go` | reading a FlexRadio's name from its API |
| `recent.go` | the "Tested before" list |
| `ui.go`, `web/index.html` | the browser page |
| `version.go` | versions and feature negotiation between copies |
| `update*.go` | signed self-update and updating the far end |
| `tools/mkwaveform` | packs the FlexRadio waveform image |
| `tools/mkmacapp` | Radio Bandwidth.app and the universal Mac program |
| `tools/release` | signing key and signed update manifest |
| `.github/workflows/release.yml` | builds, signs and publishes a release from a tag |
