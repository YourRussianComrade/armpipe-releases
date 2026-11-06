package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Adapter struct {
	Name string
	Nets []*net.IPNet
	Up   bool
}

func localAdapters() []Adapter {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Adapter
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		a := Adapter{Name: ifc.Name, Up: ifc.Flags&net.FlagUp != 0}
		for _, ad := range addrs {
			if n, ok := ad.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				a.Nets = append(a.Nets, n)
			}
		}
		if len(a.Nets) > 0 {
			out = append(out, a)
		}
	}
	return out
}

// Сетевая карта ПК, в подсети которой находится адрес устройства.
func adapterFor(adapters []Adapter, ip string) (string, string) {
	t := net.ParseIP(ip)
	if t == nil {
		return "", ""
	}
	for _, a := range adapters {
		for _, n := range a.Nets {
			if a.Up && n.Contains(t) {
				return a.Name, n.String()
			}
		}
	}
	return "", ""
}

func maskString(n *net.IPNet) string {
	if len(n.Mask) == 4 {
		return net.IP(n.Mask).String()
	}
	ones, _ := n.Mask.Size()
	return "/" + strconv.Itoa(ones)
}

type PingStats struct {
	Sent, Recv int
	Min, Avg   time.Duration
	Max        time.Duration
	LastErr    error
}

func pingN(ip string, n int, timeout time.Duration) PingStats {
	s := PingStats{Sent: n}
	var sum time.Duration
	for i := 0; i < n; i++ {
		rtt, err := pingOnce(ip, timeout)
		if err != nil {
			s.LastErr = err
			continue
		}
		s.Recv++
		sum += rtt
		if s.Recv == 1 || rtt < s.Min {
			s.Min = rtt
		}
		if rtt > s.Max {
			s.Max = rtt
		}
		if i < n-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if s.Recv > 0 {
		s.Avg = sum / time.Duration(s.Recv)
	}
	return s
}

func isRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var se *os.SyscallError
	if errors.As(err, &se) {
		if en, ok := se.Err.(syscall.Errno); ok && (en == 10061 || en == 111) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "refused")
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func describeNetErr(err error) string {
	switch {
	case err == nil:
		return ""
	case isRefused(err):
		return "соединение отклонено (порт закрыт)"
	case isTimeout(err):
		return "нет ответа (тайм-аут)"
	case errors.Is(err, syscall.ECONNRESET) || strings.Contains(err.Error(), "10054") || strings.Contains(err.Error(), "reset"):
		return "соединение сброшено устройством"
	}
	return err.Error()
}

func ms(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 мс"
	}
	return fmt.Sprintf("%d мс", d.Milliseconds())
}
