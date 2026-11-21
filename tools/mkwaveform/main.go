// mkwaveform packs a static Linux binary into a FlexRadio Docker waveform:
// an OCI image layout, gzipped, that SmartSDR's Waveform Manager installs.
//
// The image is FROM scratch: the binary (the entrypoint) plus any extra
// files, and the two labels the radio reads (com.flexradio.waveform.name and
// .version). Everything is fixed (timestamps, owners, order), so the same
// inputs always give the same bytes. No Docker daemon is needed.
//
//	go run ./tools/mkwaveform -name radio-bandwidth -version 1.6.0 -arch arm64 \
//	    -out radio-bandwidth-waveform-1.6.0.tar.gz -args=--responder \
//	    dist/waveform/radio-bandwidth NOTICES
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type file struct {
	name string
	data []byte
	mode int64
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func tarOf(files []file) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Mode: f.mode, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if f.data == nil {
			h.Typeflag = tar.TypeDir
		} else {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(f.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			panic(err)
		}
		if f.data != nil {
			if _, err := tw.Write(f.data); err != nil {
				panic(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func gzipOf(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.ModTime = time.Unix(0, 0) // no timestamp in the gzip header
	if _, err := zw.Write(b); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v) // map keys are sorted, so output is stable
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	name := flag.String("name", "", "waveform name (label com.flexradio.waveform.name)")
	version := flag.String("version", "", "waveform version (label com.flexradio.waveform.version)")
	arch := flag.String("arch", "arm64", "CPU architecture: arm64 for FLEX-8000 and Aurora")
	out := flag.String("out", "", "output .tar.gz")
	args := flag.String("args", "", "extra entrypoint arguments, comma separated")
	env := flag.String("env", "", "environment, comma separated KEY=VALUE")
	flag.Parse()
	if *name == "" || *version == "" || *out == "" || flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mkwaveform -name N -version V -out F.tar.gz [-arch arm64] [-args a,b] [-env K=V] <binary> [extra files...]")
		os.Exit(2)
	}

	var layerFiles []file
	for i, p := range flag.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		mode := int64(0o644)
		if i == 0 {
			mode = 0o755 // the entrypoint
		}
		layerFiles = append(layerFiles, file{filepath.Base(p), b, mode})
	}
	layerTar := tarOf(layerFiles)
	layer := gzipOf(layerTar)

	entry := []string{"/" + layerFiles[0].name}
	if *args != "" {
		entry = append(entry, strings.Split(*args, ",")...)
	}
	cfg := map[string]any{
		"Entrypoint": entry,
		"Labels": map[string]string{
			"com.flexradio.waveform.name":    *name,
			"com.flexradio.waveform.version": *version,
		},
	}
	if *env != "" {
		cfg["Env"] = strings.Split(*env, ",")
	}
	config := mustJSON(map[string]any{
		"architecture": *arch,
		"os":           "linux",
		"config":       cfg,
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{digest(layerTar)}},
		"history":      []map[string]string{{"created_by": "radio-bandwidth tools/mkwaveform"}},
	})
	manifest := mustJSON(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digest(config), "size": len(config)},
		"layers":        []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": digest(layer), "size": len(layer)}},
	})
	index := mustJSON(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType":   "application/vnd.oci.image.manifest.v1+json",
			"digest":      digest(manifest),
			"size":        len(manifest),
			"annotations": map[string]string{"org.opencontainers.image.ref.name": *version},
			"platform":    map[string]string{"architecture": *arch, "os": "linux"},
		}},
	})

	blobs := map[string][]byte{digest(config): config, digest(manifest): manifest, digest(layer): layer}
	var keys []string
	for k := range blobs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	outer := []file{{"blobs", nil, 0o755}, {"blobs/sha256", nil, 0o755}}
	for _, k := range keys {
		outer = append(outer, file{"blobs/sha256/" + strings.TrimPrefix(k, "sha256:"), blobs[k], 0o644})
	}
	outer = append(outer,
		file{"index.json", index, 0o644},
		file{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644})

	image := gzipOf(tarOf(outer))
	if err := os.WriteFile(*out, image, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sum := sha256.Sum256(image)
	fmt.Printf("%s  %d bytes  %s\n", hex.EncodeToString(sum[:]), len(image), *out)
}
