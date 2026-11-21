package main

// Automatic updates. When a tester finds the far end running an older
// Radio Bandwidth, it tells it to update. The far end fetches the newest release
// from GitHub, checks it, swaps its own program file and restarts on the
// same ports. Nothing is installed unless:
//
//   - the release manifest is signed with the Radio Bandwidth release key
//     (releasePublicKey; the private half is a GitHub Actions secret), and
//   - the downloaded file matches the SHA-256 the manifest lists, and
//   - the release is newer than what is running (no downgrades).
//
// So a tester can only ever trigger an update to an official, newer release;
// it cannot send code. Copies inside a FlexRadio (Docker waveform) never
// update themselves: SmartSDR's Waveform Manager owns them.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// releasePublicKey verifies release manifests. Made with
// "go run ./tools/release keygen".
const releasePublicKey = "9Xgx0D1TQziKyxOsohUkCf9PQTomAiV4ecIbkQvQGaM="

// Where releases are published. The repository must be public for copies of
// Radio Bandwidth to reach these without a login.
var releaseBaseURL = "https://github.com/va3mw/radio-bandwidth/releases/latest/download/"

func init() {
	// For testing a release before publishing it. Safe: whatever is served
	// must still be signed with the release key.
	if u := os.Getenv("RBW_RELEASE_URL"); u != "" {
		releaseBaseURL = strings.TrimSuffix(u, "/") + "/"
	}
}

type releaseManifest struct {
	Version string `json:"version"`
	Files   map[string]struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"files"`
}

var updateHTTP = &http.Client{Timeout: 60 * time.Second}

func fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "radio-bandwidth/"+appVersion)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("download too large")
	}
	return b, nil
}

// verifyManifest checks the signature and parses the manifest.
func verifyManifest(body, sigB64 []byte, pubB64 string) (*releaseManifest, error) {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("bad release key")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil || !ed25519.Verify(pub, body, sig) {
		return nil, errors.New("the release signature is not valid")
	}
	var m releaseManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// latestRelease fetches and verifies the newest published manifest.
func latestRelease(ctx context.Context) (*releaseManifest, error) {
	body, err := fetch(ctx, releaseBaseURL+"radio-bandwidth-update.json", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("could not reach the Radio Bandwidth releases on GitHub (%v)", err)
	}
	sig, err := fetch(ctx, releaseBaseURL+"radio-bandwidth-update.json.sig", 4096)
	if err != nil {
		return nil, fmt.Errorf("could not reach the Radio Bandwidth releases on GitHub (%v)", err)
	}
	return verifyManifest(body, sig, releasePublicKey)
}

// inContainer: inside a FlexRadio (or any container), updates are the
// container manager's job, not ours.
func inContainer() bool {
	if os.Getenv("SSDR_RADIO_ADDRESS") != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

type updater struct {
	mu    sync.Mutex
	state string // "", "updating", "failed: ...", "up to date"
}

func (u *updater) set(s string) {
	u.mu.Lock()
	u.state = s
	u.mu.Unlock()
}

func (u *updater) get() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// startUpdate begins a background update to at least minVersion. It returns
// an error straight away when updating is impossible here.
func (a *App) startUpdate(minVersion, reason string) error {
	if inContainer() {
		return errors.New("this copy runs inside a container (FlexRadio waveform); update it with SmartSDR's Waveform Manager")
	}
	a.mu.Lock()
	busy := a.test.Running
	a.mu.Unlock()
	if busy {
		return errors.New("busy running its own test")
	}
	if strings.HasPrefix(a.upd.get(), "updating") {
		return nil // already on its way
	}
	a.upd.set("updating")
	go func() {
		err := a.doUpdate(minVersion, reason)
		if err != nil {
			a.upd.set("failed: " + err.Error())
			fmt.Printf("[%s] Update failed: %v\n", time.Now().Format("15:04:05"), err)
		}
	}()
	return nil
}

func (a *App) doUpdate(minVersion, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	if compareVersions(m.Version, appVersion) <= 0 {
		return fmt.Errorf("already running the newest published version (%s)", m.Version)
	}
	if minVersion != "" && compareVersions(m.Version, minVersion) < 0 {
		return fmt.Errorf("the newest published version is %s, older than %s", m.Version, minVersion)
	}
	f, ok := m.Files[runtime.GOOS+"-"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("release %s has no file for %s/%s", m.Version, runtime.GOOS, runtime.GOARCH)
	}
	fmt.Printf("[%s] Updating Radio Bandwidth %s to %s (%s)\n", time.Now().Format("15:04:05"), appVersion, m.Version, reason)
	bin, err := fetch(ctx, releaseBaseURL+f.Name, 64<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != strings.ToLower(f.SHA256) {
		return errors.New("the downloaded file does not match the signed release")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	// Write next to the program, so the final swap is a rename on one disk.
	newPath := exe + ".new"
	if err := os.WriteFile(newPath, bin, 0o755); err != nil {
		return fmt.Errorf("cannot write next to %s (%v)", exe, err)
	}
	a.upd.set("restarting into " + m.Version)
	return a.swapAndRestart(exe, newPath) // only returns on failure
}

// cleanupOldBinary removes what a previous update left behind.
func cleanupOldBinary() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
		_ = os.Remove(exe + ".new")
	}
}

// restartArgs are the arguments for the updated program: the same as now,
// plus --after-update (wait for the ports, do not open another browser page).
func restartArgs() []string {
	args := []string{}
	for _, a := range os.Args[1:] {
		if a != "--after-update" && a != "-after-update" {
			args = append(args, a)
		}
	}
	return append(args, "--after-update")
}

// reconcileVersions runs before a test. If the far end is older and can
// update itself, it is told to, and the test waits for it to come back. If
// that fails for any reason, the test goes ahead with what it is running.
// Returns the far end's (possibly new) hello and notes for the report.
func (a *App) reconcileVersions(ctx context.Context, host string, port int, h *HelloReply) (*HelloReply, []string) {
	switch compareVersions(h.Version, appVersion) {
	case 0:
		return h, nil
	case 1:
		return h, []string{fmt.Sprintf("%s runs a newer Radio Bandwidth (%s) than this computer (%s). Update this computer to get the newest tests.", h.Host, h.Version, appVersion)}
	}
	if h.Radio != nil {
		return h, []string{fmt.Sprintf("%s runs Radio Bandwidth waveform %s; %s is available. Reinstall it with SmartSDR's Waveform Manager to get the newest tests.", h.Host, h.Version, appVersion)}
	}
	if !peerFeatures(h)["update"] {
		return h, []string{fmt.Sprintf("%s runs Radio Bandwidth %s, which cannot update itself. Install %s there once; after that it keeps itself up to date.", h.Host, h.Version, appVersion)}
	}
	a.setPhase("update", fmt.Sprintf("Updating Radio Bandwidth on %s from %s to %s", h.Host, h.Version, appVersion))
	nh, err := a.updatePeer(ctx, host, port, h)
	if err != nil {
		return nh, []string{fmt.Sprintf("Could not update Radio Bandwidth on %s (%v), so it was tested with the version it runs (%s).", nh.Host, err, nh.Version)}
	}
	return nh, []string{fmt.Sprintf("Updated Radio Bandwidth on %s from %s to %s before testing.", nh.Host, h.Version, nh.Version)}
}

// updatePeer asks the far end to update and waits up to 90 seconds for it
// to come back with at least our version. It always returns the last hello
// it saw, so the test can carry on either way.
func (a *App) updatePeer(ctx context.Context, host string, port int, h *HelloReply) (*HelloReply, error) {
	c, br, err := a.dialOp(ctx, host, port, Request{Op: "update", Version: appVersion, FromHost: a.hostName()})
	if err != nil {
		return h, err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var rep struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	err = readJSONLine(br, &rep, 1<<16)
	c.Close()
	if err != nil {
		return h, err
	}
	if !rep.OK {
		return h, errors.New(rep.Reason)
	}
	last := h
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		a.tick(-1, -1)
		nh, err := a.hello(host, port, 2*time.Second)
		if err != nil {
			continue // restarting
		}
		last = nh
		if compareVersions(nh.Version, appVersion) >= 0 {
			return nh, nil
		}
		if strings.HasPrefix(nh.UpdateState, "failed: ") {
			return nh, errors.New(strings.TrimPrefix(nh.UpdateState, "failed: "))
		}
	}
	return last, errors.New("it did not come back updated within 90 seconds")
}

// releaseCheckLoop looks for a newer release now and then, for the "update
// available" banner on this computer.
func (a *App) releaseCheckLoop() {
	if inContainer() {
		return
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		m, err := latestRelease(ctx)
		cancel()
		if err == nil && compareVersions(m.Version, appVersion) > 0 {
			a.mu.Lock()
			a.latest = m.Version
			a.mu.Unlock()
		}
		time.Sleep(6 * time.Hour)
	}
}
