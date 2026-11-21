package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.7.0", "1.7.0", 0}, {"1.6.0", "1.7.0", -1}, {"1.10.0", "1.9.9", 1},
		{"v1.7.1", "1.7.0", 1}, {"2.0", "1.99.99", 1}, {"1.7.0-beta", "1.7.0", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// Copies from before 1.7.0 do not list features: infer from the version.
func TestPeerFeaturesFromOldVersions(t *testing.T) {
	old := peerFeatures(&HelloReply{Version: "1.1.0"})
	if old["udpstream"] || old["mtu"] || old["update"] || !old["tcp"] {
		t.Errorf("1.1.0: %v", old)
	}
	mid := peerFeatures(&HelloReply{Version: "1.5.0"})
	if !mid["udpstream"] || !mid["mtu"] || mid["update"] {
		t.Errorf("1.5.0: %v", mid)
	}
	now := peerFeatures(&HelloReply{Version: "1.7.0", Features: myFeatures})
	if !now["update"] {
		t.Errorf("1.7.0: %v", now)
	}
}

func TestVerifyManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	body := []byte(`{"version":"1.7.1","files":{"linux-amd64":{"name":"radio-bandwidth-linux-amd64","sha256":"ab","size":1}}}`)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))

	m, err := verifyManifest(body, sig, pubB64)
	if err != nil || m.Version != "1.7.1" || m.Files["linux-amd64"].Name != "radio-bandwidth-linux-amd64" {
		t.Fatalf("good manifest rejected: %v %+v", err, m)
	}
	tampered := []byte(strings.Replace(string(body), "1.7.1", "9.9.9", 1))
	if _, err := verifyManifest(tampered, sig, pubB64); err == nil {
		t.Fatal("tampered manifest accepted")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := verifyManifest(body, sig, base64.StdEncoding.EncodeToString(otherPub)); err == nil {
		t.Fatal("manifest signed by another key accepted")
	}
}

// The embedded key must be a real Ed25519 public key.
func TestReleaseKeyIsValid(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(releasePublicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		t.Fatalf("releasePublicKey is not a valid Ed25519 key")
	}
}

// The update notes and skipped tests must reach the report.
func TestVersionNotesInReport(t *testing.T) {
	r := &Result{LocalKind: "wired", RemoteKind: "wired", RemoteHost: "OldPC", RemoteVersion: "1.1.0",
		LocalLinkMbps: 1000, RemoteLinkMbps: 1000, Route: "local", DownMbps: 930, UpMbps: 930, IdleRTT: 0.3,
		LatencyMethod: "UDP", Sent: 300, Skipped: []string{"mtu", "udpstream"},
		VersionNotes: []string{"OldPC runs Radio Bandwidth 1.1.0, which cannot update itself."}}
	r.Grade("auto")
	txt := r.Text()
	for _, want := range []string{"cannot update itself", "Not tested: the packet size (MTU) test and the UDP stream test"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report misses %q:\n%s", want, txt)
		}
	}
	if r.Verdict != "good" {
		t.Errorf("skipped tests must not make the verdict worse: %s", r.Verdict)
	}
}
