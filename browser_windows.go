//go:build windows

package main

import "golang.org/x/sys/windows"

// openBrowser asks Windows to open the default browser, the same way
// double-clicking a link does. Radio Bandwidth never starts other programs itself.
func openBrowser(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	u, _ := windows.UTF16PtrFromString(url)
	_ = windows.ShellExecute(0, verb, u, nil, nil, windows.SW_SHOWNORMAL)
}
