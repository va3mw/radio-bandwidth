package main

// Updating another computer on request: the Update button next to a
// computer in the list. The same signed update as before a test, just
// started by hand.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// peerVer is what another copy last said about itself.
type peerVer struct {
	version   string
	canUpdate bool
	at        time.Time
}

// verKey identifies another copy by the address and port it answers on.
func (a *App) verKey(target string) string {
	host, port := a.splitTarget(target)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// notePeerVersion remembers the version a hello reported.
func (a *App) notePeerVersion(key string, h *HelloReply) {
	if h == nil || h.ID == a.id {
		return
	}
	a.mu.Lock()
	a.pver[key] = peerVer{version: h.Version, canUpdate: peerFeatures(h)["update"], at: time.Now()}
	a.mu.Unlock()
}

// refreshPeerVersion asks a LAN peer for its version now and then (the LAN
// announcement does not carry it). Never during a test.
func (a *App) refreshPeerVersion(target string) {
	key := a.verKey(target)
	a.mu.Lock()
	v, ok := a.pver[key]
	stale := !ok || time.Since(v.at) > 30*time.Second
	if !stale || a.test.Running {
		a.mu.Unlock()
		return
	}
	v.at = time.Now() // one lookup at a time
	a.pver[key] = v
	a.mu.Unlock()
	go func() {
		host, port := a.splitTarget(target)
		if h, err := a.hello(host, port, 1500*time.Millisecond); err == nil {
			a.notePeerVersion(key, h)
		}
	}()
}

// peerUpdateInfo says what the page should offer for another copy:
// "" (nothing), "available", "manual" (too old to update itself),
// "updating", "updated" or "failed: why".
func (a *App) peerUpdateInfo(target string) (version, state string) {
	key := a.verKey(target)
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.pver[key]
	if s, ok := a.pupd[key]; ok {
		return v.version, s
	}
	if v.version == "" || compareVersions(v.version, appVersion) >= 0 {
		return v.version, ""
	}
	if v.canUpdate {
		return v.version, "available"
	}
	return v.version, "manual"
}

// startPeerUpdate checks the other copy can be updated, then updates it in
// the background. Progress shows in the computer list.
func (a *App) startPeerUpdate(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("no computer given")
	}
	key := a.verKey(target)
	a.mu.Lock()
	busy, state := a.test.Running, a.pupd[key]
	a.mu.Unlock()
	if busy {
		return errors.New("wait for the test to finish")
	}
	if state == "updating" {
		return nil
	}
	host, port := a.splitTarget(target)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ip, _, err := resolveTarget(ctx, host)
	cancel()
	if err != nil {
		return err
	}
	h, err := a.hello(ip, port, 3*time.Second)
	if err != nil {
		return fmt.Errorf("%s is not answering: %v", host, err)
	}
	a.notePeerVersion(key, h)
	switch {
	case compareVersions(h.Version, appVersion) >= 0:
		return fmt.Errorf("%s already runs Radio Bandwidth %s", h.Host, h.Version)
	case !peerFeatures(h)["update"]:
		return fmt.Errorf("%s runs Radio Bandwidth %s, which cannot update itself. Install %s there once; after that it keeps itself up to date", h.Host, h.Version, appVersion)
	}
	a.mu.Lock()
	a.pupd[key] = "updating"
	a.mu.Unlock()
	fmt.Printf("[%s] Updating Radio Bandwidth on %s from %s to %s\n", time.Now().Format("15:04:05"), h.Host, h.Version, appVersion)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		nh, err := a.updatePeer(ctx, ip, port, h)
		a.notePeerVersion(key, nh)
		st := "updated"
		if err != nil {
			st = "failed: " + err.Error()
			fmt.Printf("[%s] Could not update Radio Bandwidth on %s: %v\n", time.Now().Format("15:04:05"), h.Host, err)
		} else {
			fmt.Printf("[%s] Updated Radio Bandwidth on %s to %s\n", time.Now().Format("15:04:05"), nh.Host, nh.Version)
		}
		a.mu.Lock()
		a.pupd[key] = st
		a.mu.Unlock()
		time.Sleep(time.Minute) // show the outcome for a while
		a.mu.Lock()
		if a.pupd[key] == st {
			delete(a.pupd, key)
		}
		a.mu.Unlock()
	}()
	return nil
}
