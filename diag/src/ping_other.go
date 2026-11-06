//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

var rttRe = regexp.MustCompile(`time[=<]([\d.]+) ?ms`)

// Для проверки на Linux/macOS — через системный ping.
func pingOnce(ip string, timeout time.Duration) (time.Duration, error) {
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	out, err := exec.Command("ping", "-c", "1", "-W", strconv.Itoa(secs), ip).CombinedOutput()
	if err != nil {
		return 0, errors.New("нет ответа")
	}
	if m := rttRe.FindSubmatch(out); m != nil {
		f, _ := strconv.ParseFloat(string(m[1]), 64)
		return time.Duration(f * float64(time.Millisecond)), nil
	}
	return 0, nil
}
