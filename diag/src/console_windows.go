//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procSetConsoleTitleW   = kernel32.NewProc("SetConsoleTitleW")
)

// UTF-8 для кириллицы и ANSI-цвета (Windows 10+). Возвращает true, если цвета доступны.
func setupConsole(title string) bool {
	procSetConsoleOutputCP.Call(65001)
	procSetConsoleCP.Call(65001)
	if t, err := syscall.UTF16PtrFromString(title); err == nil {
		procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(t)))
	}
	h := os.Stdout.Fd()
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	const enableVT = 0x0004
	r, _, _ := procSetConsoleMode.Call(h, uintptr(mode|enableVT))
	return r != 0
}
