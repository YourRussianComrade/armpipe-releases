//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"syscall"
	"time"
	"unsafe"
)

var (
	iphlpapi            = syscall.NewLazyDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

// ping через IcmpSendEcho — работает без прав администратора.
func pingOnce(ip string, timeout time.Duration) (time.Duration, error) {
	addr := net.ParseIP(ip).To4()
	if addr == nil {
		ips, err := net.LookupIP(ip)
		if err != nil || len(ips) == 0 || ips[0].To4() == nil {
			return 0, errors.New("неверный IPv4-адрес")
		}
		addr = ips[0].To4()
	}
	h, _, err := procIcmpCreateFile.Call()
	if h == 0 || h == ^uintptr(0) {
		return 0, err
	}
	defer procIcmpCloseHandle.Call(h)

	data := []byte("ArmPipeDiag-ping")
	reply := make([]byte, 256)
	dst := binary.LittleEndian.Uint32(addr)
	start := time.Now()
	n, _, _ := procIcmpSendEcho.Call(h, uintptr(dst),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeout.Milliseconds()))
	elapsed := time.Since(start)
	if n == 0 {
		return 0, errors.New("нет ответа")
	}
	status := binary.LittleEndian.Uint32(reply[4:8])
	if status != 0 {
		switch status {
		case 11003:
			return 0, errors.New("узел недоступен")
		case 11002:
			return 0, errors.New("сеть недоступна")
		case 11010:
			return 0, errors.New("превышен интервал ожидания")
		}
		return 0, errors.New("ошибка ICMP")
	}
	rtt := time.Duration(binary.LittleEndian.Uint32(reply[8:12])) * time.Millisecond
	if rtt == 0 && elapsed < time.Millisecond {
		rtt = elapsed
	}
	return rtt, nil
}
