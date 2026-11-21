package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

var httpClient = http.Client{Timeout: 2 * time.Second}

func parseIP(s string) net.IP { return net.ParseIP(s) }

type uiState struct {
	App        string      `json:"app"`
	Version    string      `json:"version"`
	Host       string      `json:"host"`
	OS         string      `json:"os"`
	Interfaces []IfaceInfo `json:"interfaces"`
	BindIP     string      `json:"bindIP"`
	Profile    string      `json:"profile"`
	Profiles   []Profile   `json:"profiles"`
	Peers      []Peer      `json:"peers"`
	Recent     []Recent    `json:"recent"`
	Latest     string      `json:"latest"`      // newer release available, if any
	UpdState   string      `json:"updateState"` // progress of updating this computer
	Test       TestState   `json:"test"`
	Result     *Result     `json:"result"`
	Incoming   *Incoming   `json:"incoming"`
}

func (a *App) startUI(port int) (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	for try := 0; err != nil && a.afterUpdate && try < 20; try++ {
		time.Sleep(500 * time.Millisecond) // the old copy is still letting go
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	}
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, appName) })
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		st := uiState{App: appName, Version: appVersion, Host: a.hostName(), OS: runtime.GOOS,
			Interfaces: a.interfaces(), Peers: a.peerList(), Recent: a.recent.snapshot(), Test: a.snapshotTest()}
		for i := range st.Peers {
			st.Peers[i].Version, st.Peers[i].Update = a.peerUpdateInfo(st.Peers[i].Addr)
		}
		for i := range st.Recent {
			if st.Recent[i].Online {
				st.Recent[i].Version, st.Recent[i].Update = a.peerUpdateInfo(st.Recent[i].Target)
			}
		}
		for _, k := range profileOrder {
			st.Profiles = append(st.Profiles, profiles[k])
		}
		a.mu.Lock()
		st.BindIP, st.Profile, st.Latest = a.bindIP, a.profile, a.latest
		st.UpdState = a.upd.get()
		if a.lastRes != nil {
			r := *a.lastRes
			st.Result = &r
		}
		if a.incoming != nil {
			in := *a.incoming
			if in.Result != nil {
				r := *in.Result
				in.Result = &r
			}
			st.Incoming = &in
		}
		a.mu.Unlock()
		writeJSON(w, st)
	})
	mux.HandleFunc("/api/bind", a.post(func(body map[string]string) error {
		ip := body["ip"]
		if ip != "" && net.ParseIP(ip) == nil {
			return fmt.Errorf("bad address")
		}
		a.mu.Lock()
		a.bindIP = ip
		a.mu.Unlock()
		return nil
	}))
	mux.HandleFunc("/api/profile", a.post(func(body map[string]string) error {
		p := body["profile"]
		if _, ok := profiles[p]; !ok && p != "auto" {
			return fmt.Errorf("unknown profile")
		}
		a.mu.Lock()
		a.profile = p
		if a.lastRes != nil {
			a.lastRes.Grade(p)
		}
		if a.incoming != nil && a.incoming.Result != nil {
			a.incoming.Result.Grade(p)
		}
		a.mu.Unlock()
		return nil
	}))
	mux.HandleFunc("/api/start", a.post(func(body map[string]string) error {
		t := strings.TrimSpace(body["target"])
		if t == "" {
			return fmt.Errorf("enter the other computer's address")
		}
		a.mu.Lock()
		busy := a.test.Running
		a.mu.Unlock()
		if busy {
			return fmt.Errorf("a test is already running")
		}
		go func() {
			res, err := a.runTest(context.Background(), t)
			if err == nil {
				fmt.Println(res.Text())
			} else {
				fmt.Println("Test failed:", err)
			}
		}()
		return nil
	}))
	mux.HandleFunc("/api/cancel", a.post(func(map[string]string) error {
		a.mu.Lock()
		if a.cancel != nil {
			a.cancel()
		}
		a.mu.Unlock()
		return nil
	}))
	mux.HandleFunc("/api/quit", a.post(func(map[string]string) error {
		go func() {
			time.Sleep(300 * time.Millisecond) // let the reply reach the page
			fmt.Println("Stopped from the browser page.")
			os.Exit(0)
		}()
		return nil
	}))
	mux.HandleFunc("/api/update", a.post(func(map[string]string) error {
		return a.startUpdate("", "requested on this computer")
	}))
	mux.HandleFunc("/api/update-peer", a.post(func(body map[string]string) error {
		return a.startPeerUpdate(body["target"])
	}))
	mux.HandleFunc("/api/forget", a.post(func(body map[string]string) error {
		a.recent.remove(body["target"])
		return nil
	}))
	mux.HandleFunc("/api/dismiss", a.post(func(map[string]string) error {
		a.mu.Lock()
		a.incoming = nil
		a.mu.Unlock()
		return nil
	}))
	mux.HandleFunc("/api/report.txt", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		res := a.lastRes
		if r.URL.Query().Get("which") == "incoming" && a.incoming != nil {
			res = a.incoming.Result
		}
		a.mu.Unlock()
		if res == nil {
			http.Error(w, "no results yet", 404)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"radio-bandwidth-"+strings.NewReplacer(" ", "_", ":", "").Replace(res.Time)+".txt\"")
		fmt.Fprint(w, res.Text())
	})

	srv := &http.Server{Handler: guardLocal(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/", nil
}

// guardLocal blocks web pages on other sites from driving the local UI.
func guardLocal(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
			http.Error(w, "forbidden", 403)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (a *App) post(fn func(map[string]string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		body := map[string]string{}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		if err := fn(body); err != nil {
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"ok": "1"})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
