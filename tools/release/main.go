// release: signing for Radio Bandwidth's automatic updates.
//
//	go run ./tools/release keygen
//	    Makes a new Ed25519 key pair. Prints the public key (goes into
//	    update.go as releasePublicKey) and the private key (store it as the
//	    GitHub Actions secret RBW_SIGNING_KEY, and keep a backup).
//
//	RBW_SIGNING_KEY=... go run ./tools/release manifest -version 1.7.0 -dir dist
//	    Writes dist/radio-bandwidth-update.json (version, file names, SHA-256) and
//	    dist/radio-bandwidth-update.json.sig (Ed25519 signature of that file).
//
// Radio Bandwidth only installs an update whose manifest is signed by this key and
// whose file matches the SHA-256 in the manifest.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Release files per platform, as build.sh names them.
var assets = map[string]string{
	"windows-amd64": "radio-bandwidth.exe",
	"windows-arm64": "radio-bandwidth-windows-arm64.exe",
	"linux-amd64":   "radio-bandwidth-linux-amd64",
	"linux-arm64":   "radio-bandwidth-linux-arm64",
	"darwin-arm64":  "radio-bandwidth-mac-arm64",
	"darwin-amd64":  "radio-bandwidth-mac-intel",
}

type assetInfo struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version string               `json:"version"`
	Files   map[string]assetInfo `json:"files"`
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: release keygen | release manifest -version V -dir DIR")
	}
	switch os.Args[1] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			die("%v", err)
		}
		fmt.Println("public key (put in update.go, releasePublicKey):")
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		fmt.Println()
		fmt.Println("private key (GitHub secret RBW_SIGNING_KEY; keep a backup; never commit):")
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Seed()))

	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ExitOnError)
		version := fs.String("version", "", "release version, e.g. 1.7.0")
		dir := fs.String("dir", "dist", "folder with the built files")
		_ = fs.Parse(os.Args[2:])
		if *version == "" {
			die("-version is required")
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("RBW_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			die("RBW_SIGNING_KEY must hold the base64 private key from keygen")
		}
		priv := ed25519.NewKeyFromSeed(seed)

		m := manifest{Version: strings.TrimPrefix(*version, "v"), Files: map[string]assetInfo{}}
		for plat, name := range assets {
			b, err := os.ReadFile(filepath.Join(*dir, name))
			if err != nil {
				die("missing %s: %v", name, err)
			}
			sum := sha256.Sum256(b)
			m.Files[plat] = assetInfo{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
		}
		body, _ := json.MarshalIndent(m, "", "  ")
		sig := ed25519.Sign(priv, body)
		if err := os.WriteFile(filepath.Join(*dir, "radio-bandwidth-update.json"), body, 0o644); err != nil {
			die("%v", err)
		}
		if err := os.WriteFile(filepath.Join(*dir, "radio-bandwidth-update.json.sig"), []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
			die("%v", err)
		}
		fmt.Printf("signed manifest for %s with %d files\n", m.Version, len(m.Files))

	default:
		die("unknown command %q", os.Args[1])
	}
}
