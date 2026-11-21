package main

// Versions and features. Two copies of Radio Bandwidth do not need to be the same
// version: each tells the other what it can do, and a test simply skips what
// the far end does not support. Copies from before this scheme (1.5.x and
// older) are recognised by their version number.

import (
	"strconv"
	"strings"
)

// protoVersion changes only when the wire protocol changes incompatibly.
const protoVersion = 2

// What this copy can do, sent in every hello.
var myFeatures = []string{"latency", "tcp", "mtu", "udpstream", "report", "maxmbps", "update"}

// compareVersions returns -1, 0 or 1 for "1.5.0" style versions.
func compareVersions(a, b string) int {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func splitVersion(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, p := range strings.SplitN(v, ".", 3) {
		n, _ := strconv.Atoi(strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}

// peerFeatures is what the far end can do: what it says, or, for copies too
// old to say, what its version number implies.
func peerFeatures(h *HelloReply) map[string]bool {
	f := map[string]bool{}
	if len(h.Features) > 0 {
		for _, x := range h.Features {
			f[x] = true
		}
		return f
	}
	f["latency"], f["tcp"], f["report"] = true, true, true
	if compareVersions(h.Version, "1.2.0") >= 0 {
		f["udpstream"], f["mtu"] = true, true
	}
	if compareVersions(h.Version, "1.6.0") >= 0 {
		f["maxmbps"] = true
	}
	return f
}

// featureNames is how a skipped test is described to the user.
var featureNames = map[string]string{
	"udpstream": "the UDP stream test",
	"mtu":       "the packet size (MTU) test",
}
