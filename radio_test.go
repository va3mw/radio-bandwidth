package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseKV(t *testing.T) {
	kv := parseKV(`model="FLEX-8600",chassis_serial="1234-5678",name=Shack,callsign=VA3MW,location="Ottawa, ON",gps=Not Present`)
	want := map[string]string{"model": "FLEX-8600", "chassis_serial": "1234-5678", "name": "Shack", "callsign": "VA3MW", "location": "Ottawa, ON", "gps": "Not Present"}
	for k, v := range want {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
}

func TestRadioName(t *testing.T) {
	cases := map[RadioInfo]string{
		{Model: "FLEX-8600", Nickname: "Shack", Callsign: "VA3MW"}: "FLEX-8600 Shack (VA3MW)",
		{Model: "FLEX-8600", Nickname: "FLEX-8600"}:                "FLEX-8600",
		{Model: "AU-520", Callsign: "VA3MW"}:                       "AU-520 (VA3MW)",
		{}:                                                         "FlexRadio",
	}
	for ri, want := range cases {
		if got := ri.Name(); got != want {
			t.Errorf("%+v -> %q, want %q", ri, got, want)
		}
	}
}

// fakeRadio speaks just enough of the SmartSDR API: greeting, a status line,
// then the reply to "info".
func fakeRadio(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprintf(c, "V1.4.0.0\nH5A3B2C1D\nS5A3B2C1D|radio slices=4\n")
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.TrimSpace(line) == "C1|info" {
					fmt.Fprintf(c, "S0|interlock state=READY\nR1|0|model=\"FLEX-8600\",chassis_serial=\"2420-1234\",name=Shack,callsign=VA3MW,num_scu=2\n")
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestQueryRadioInfo(t *testing.T) {
	ri, err := queryRadioInfo(fakeRadio(t), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ri.Name() != "FLEX-8600 Shack (VA3MW)" || ri.Serial != "2420-1234" {
		t.Fatalf("%+v", ri)
	}
}
