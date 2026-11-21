package main

// Running inside a FlexRadio as a Docker waveform: learn the radio's model,
// nickname and callsign from its own API (read only), so Radio Bandwidth announces
// itself as "FLEX-8600 Shack (VA3MW)" instead of a container ID.
//
// SmartSDR API: TCP 4992, line based. The radio greets with V<version> and
// H<handle>; a command is "C<seq>|<command>" and its reply
// "R<seq>|<hex status>|<key=value,...>". Status lines (S...) are ignored.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

type RadioInfo struct {
	Model    string `json:"model"`
	Nickname string `json:"nickname"`
	Callsign string `json:"callsign"`
	Serial   string `json:"serial"`
}

// Name is how the radio is shown to users.
func (ri RadioInfo) Name() string {
	s := ri.Model
	if ri.Nickname != "" && !strings.EqualFold(ri.Nickname, ri.Model) {
		s = strings.TrimSpace(s + " " + ri.Nickname)
	}
	if ri.Callsign != "" {
		s += " (" + ri.Callsign + ")"
	}
	if s == "" {
		s = "FlexRadio"
	}
	return s
}

// parseKV splits `model="FLEX-8600",name=Shack,callsign=VA3MW` into a map.
// Values may be quoted; commas inside quotes are kept.
func parseKV(s string) map[string]string {
	out := map[string]string{}
	var key, val strings.Builder
	inKey, inQuote := true, false
	flush := func() {
		k := strings.TrimSpace(key.String())
		if k != "" {
			out[strings.ToLower(k)] = strings.TrimSpace(val.String())
		}
		key.Reset()
		val.Reset()
		inKey = true
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			flush()
		case r == '=' && inKey && !inQuote:
			inKey = false
		case inKey:
			key.WriteRune(r)
		default:
			val.WriteRune(r)
		}
	}
	flush()
	return out
}

// queryRadioInfo asks the radio API (port 4992 unless addr names one) for
// "info" and returns what it says.
func queryRadioInfo(addr string, timeout time.Duration) (RadioInfo, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "4992")
	}
	return queryRadioInfoAt(addr, timeout)
}

func queryRadioInfoAt(hostport string, timeout time.Duration) (RadioInfo, error) {
	c, err := net.DialTimeout("tcp", hostport, timeout)
	if err != nil {
		return RadioInfo{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	br := bufio.NewReader(c)
	// Wait for the greeting (the handle line) before sending a command.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return RadioInfo{}, err
		}
		if strings.HasPrefix(line, "H") {
			break
		}
	}
	if _, err := fmt.Fprintf(c, "C1|info\n"); err != nil {
		return RadioInfo{}, err
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return RadioInfo{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "R1|") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 || strings.Trim(parts[1], "0") != "" {
			return RadioInfo{}, fmt.Errorf("radio refused info: %s", line)
		}
		kv := parseKV(parts[2])
		return RadioInfo{Model: kv["model"], Nickname: kv["name"], Callsign: kv["callsign"], Serial: kv["chassis_serial"]}, nil
	}
}

// radioLoop keeps the announced name in step with the radio. The container
// starts before the radio API listens, so it retries quietly until it works,
// then refreshes now and then in case the nickname or callsign changes.
func (a *App) radioLoop(addr, fixedName string) {
	logged := false
	for {
		ri, err := queryRadioInfo(addr, 5*time.Second)
		if err == nil {
			a.mu.Lock()
			a.radio = &ri
			if fixedName == "" {
				a.host = ri.Name()
			}
			name := a.host
			a.mu.Unlock()
			if !logged {
				fmt.Printf("[%s] Radio API at %s: announcing as %q\n", time.Now().Format("15:04:05"), addr, name)
				logged = true
			}
			time.Sleep(5 * time.Minute)
			continue
		}
		time.Sleep(15 * time.Second)
	}
}
