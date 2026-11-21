//go:build windows

package main

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// swapAndRestart: a running .exe cannot be overwritten on Windows, but it can
// be renamed. Move it aside, put the new one in its place, start it the way
// Explorer would (ShellExecute, so Radio Bandwidth itself never spawns processes),
// then exit so it can take over the ports.
func (a *App) swapAndRestart(exe, newPath string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(old, exe) // put it back
		return err
	}
	var quoted []string
	for _, x := range restartArgs() {
		quoted = append(quoted, syscall.EscapeArg(x))
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err := windows.ShellExecute(0, verb, file, params, nil, windows.SW_SHOWNORMAL); err != nil {
		_ = os.Rename(exe, newPath)
		_ = os.Rename(old, exe)
		return err
	}
	os.Exit(0)
	return nil
}
