// mkmacapp builds the macOS download: Radio Bandwidth.app with one universal
// program (Apple Silicon + Intel), zipped. Runs on any OS; no Apple tools.
//
//	go run ./tools/mkmacapp -version 1.7.0 -icon winres/icon.png \
//	    -arm64 dist/radio-bandwidth-mac-arm64 -amd64 dist/radio-bandwidth-mac-intel \
//	    -universal dist/radio-bandwidth-mac-universal -out dist/RadioBandwidth-mac.zip
//
// The app has no Dock icon (LSUIElement): Radio Bandwidth's window is the browser
// page, which has a Quit button. Opening the app again just reopens the page.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// ---- universal ("fat") Mach-O

const fatMagic = 0xCAFEBABE

func machoCPU(b []byte) (cpu, sub uint32, err error) {
	if len(b) < 12 || binary.LittleEndian.Uint32(b) != 0xFEEDFACF {
		return 0, 0, fmt.Errorf("not a 64-bit Mach-O program")
	}
	return binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:]) & 0x00FFFFFF, nil
}

func universal(slices ...[]byte) ([]byte, error) {
	const align = 14 // 16 KB, the Apple Silicon page size
	var hdr bytes.Buffer
	binary.Write(&hdr, binary.BigEndian, uint32(fatMagic))
	binary.Write(&hdr, binary.BigEndian, uint32(len(slices)))
	off := uint32(1 << align)
	var body bytes.Buffer
	for _, s := range slices {
		cpu, sub, err := machoCPU(s)
		if err != nil {
			return nil, err
		}
		for _, v := range []uint32{cpu, sub, off, uint32(len(s)), align} {
			binary.Write(&hdr, binary.BigEndian, v)
		}
		body.Write(s)
		end := off + uint32(len(s))
		next := (end + (1 << align) - 1) &^ ((1 << align) - 1)
		body.Write(make([]byte, next-end))
		off = next
	}
	out := make([]byte, 1<<align)
	copy(out, hdr.Bytes())
	return append(out, body.Bytes()...), nil
}

// ---- icon: an .icns holding the 256x256 PNG

func icns(png []byte) []byte {
	var e bytes.Buffer
	e.WriteString("ic08") // 256x256, PNG data allowed
	binary.Write(&e, binary.BigEndian, uint32(8+len(png)))
	e.Write(png)
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+e.Len()))
	out.Write(e.Bytes())
	return out.Bytes()
}

func infoPlist(version string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Radio Bandwidth</string>
	<key>CFBundleDisplayName</key><string>Radio Bandwidth</string>
	<key>CFBundleIdentifier</key><string>io.github.va3mw.radio-bandwidth</string>
	<key>CFBundleExecutable</key><string>radio-bandwidth</string>
	<key>CFBundleIconFile</key><string>RadioBandwidth</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + version + `</string>
	<key>CFBundleVersion</key><string>` + version + `</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSLocalNetworkUsageDescription</key><string>Radio Bandwidth tests the network between this Mac and other computers and radios on your local network.</string>
</dict>
</plist>
`)
}

const firstOpen = `Opening Radio Bandwidth on a Mac for the first time
=============================================

1. Drag Radio Bandwidth.app to your Applications folder (or anywhere you like).
2. Double-click it. Radio Bandwidth opens in your web browser. There is no Dock
   icon: the browser page is Radio Bandwidth's window, and its Quit button stops it.

"Radio Bandwidth cannot be opened because Apple cannot check it for malicious
software" (or "not opened"):
   Radio Bandwidth is not yet signed by Apple. Open System Settings > Privacy &
   Security, scroll down, and click "Open Anyway" next to the Radio Bandwidth
   message, then confirm. This is needed only once.

"Allow Radio Bandwidth to find devices on local networks?"
   Click Allow. Radio Bandwidth needs this to test with other computers and radios.

"Do you want Radio Bandwidth to accept incoming network connections?"
   Click Allow, so other computers can test with this Mac.
`

// ---- zip, keeping the program's execute permission

type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func zipOf(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // fixed, for repeatable output
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: stamp}
		h.SetMode(e.mode)
		if e.mode.IsDir() {
			h.Name += "/"
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if e.data != nil {
			if _, err := w.Write(e.data); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func main() {
	version := flag.String("version", "", "app version")
	icon := flag.String("icon", "winres/icon.png", "256x256 PNG icon")
	arm := flag.String("arm64", "", "Apple Silicon program")
	intel := flag.String("amd64", "", "Intel program")
	uni := flag.String("universal", "", "also write the universal program here (optional)")
	out := flag.String("out", "", "output zip")
	flag.Parse()
	if *version == "" || *arm == "" || *intel == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: mkmacapp -version V -arm64 F -amd64 F -out F.zip [-universal F] [-icon F.png]")
		os.Exit(2)
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return b
	}
	prog, err := universal(read(*arm), read(*intel))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *uni != "" {
		if err := os.WriteFile(*uni, prog, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	dir := fs.ModeDir | 0o755
	z, err := zipOf([]entry{
		{"Radio Bandwidth.app", nil, dir},
		{"Radio Bandwidth.app/Contents", nil, dir},
		{"Radio Bandwidth.app/Contents/Info.plist", infoPlist(*version), 0o644},
		{"Radio Bandwidth.app/Contents/MacOS", nil, dir},
		{"Radio Bandwidth.app/Contents/MacOS/radio-bandwidth", prog, 0o755},
		{"Radio Bandwidth.app/Contents/Resources", nil, dir},
		{"Radio Bandwidth.app/Contents/Resources/RadioBandwidth.icns", icns(read(*icon)), 0o644},
		{"Opening Radio Bandwidth the first time.txt", []byte(firstOpen), 0o644},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, z, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s: Radio Bandwidth.app %s, universal program %d bytes\n", *out, *version, len(prog))
}
