package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type rtspResponse struct {
	Code    int
	Status  string
	Headers textproto.MIMEHeader
	Body    []byte
}

func rtspRequest(conn net.Conn, r *bufio.Reader, method, uri string, cseq int, extra map[string]string, timeout time.Duration) (*rtspResponse, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: ArmPipe-Diag\r\n", method, uri, cseq)
	for k, v := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	tp := textproto.NewReader(r)
	line, err := tp.ReadLine()
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return nil, fmt.Errorf("ответ не похож на RTSP: %q", line)
	}
	code, _ := strconv.Atoi(parts[1])
	status := ""
	if len(parts) == 3 {
		status = parts[2]
	}
	hdr, err := tp.ReadMIMEHeader()
	if err != nil && len(hdr) == 0 {
		return nil, err
	}
	resp := &rtspResponse{Code: code, Status: status, Headers: hdr}
	if cl, _ := strconv.Atoi(hdr.Get("Content-Length")); cl > 0 && cl < 1<<20 {
		resp.Body = make([]byte, cl)
		if _, err := io.ReadFull(r, resp.Body); err != nil {
			return resp, nil
		}
	}
	return resp, nil
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitAuth(s) {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			out[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
	}
	return out
}

func splitAuth(s string) []string {
	var parts []string
	var cur strings.Builder
	inQ := false
	for _, ch := range s {
		switch {
		case ch == '"':
			inQ = !inQ
			cur.WriteRune(ch)
		case ch == ',' && !inQ:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	return append(parts, cur.String())
}

func buildAuth(challenges []string, user, pass, method, uri string) string {
	for _, c := range challenges {
		if strings.HasPrefix(strings.ToLower(c), "digest") {
			p := parseAuthParams(c[len("digest"):])
			ha1 := md5hex(user + ":" + p["realm"] + ":" + pass)
			ha2 := md5hex(method + ":" + uri)
			if strings.Contains(p["qop"], "auth") {
				cnonce, nc := "0a4f113b", "00000001"
				resp := md5hex(ha1 + ":" + p["nonce"] + ":" + nc + ":" + cnonce + ":auth:" + ha2)
				return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", qop=auth, nc=%s, cnonce="%s"`,
					user, p["realm"], p["nonce"], uri, resp, nc, cnonce)
			}
			resp := md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
			return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
				user, p["realm"], p["nonce"], uri, resp)
		}
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

type RTSPResult struct {
	Code   int
	Status string
	Video  string // кодек из SDP, если удалось понять
	Err    error
}

// Проверка RTSP-потока: DESCRIBE с авторизацией (как это сделает плеер в ArmPipe).
func checkRTSP(cam CameraSettings, path string, timeout time.Duration) RTSPResult {
	addr := net.JoinHostPort(cam.IP, strconv.Itoa(cam.RTSPPort))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return RTSPResult{Err: err}
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	uri := fmt.Sprintf("rtsp://%s/%s", addr, strings.TrimLeft(path, "/"))

	resp, err := rtspRequest(conn, r, "DESCRIBE", uri, 1, map[string]string{"Accept": "application/sdp"}, timeout)
	if err != nil {
		return RTSPResult{Err: err}
	}
	if resp.Code == 401 {
		ch := resp.Headers.Values("Www-Authenticate")
		auth := buildAuth(ch, cam.User, cam.Password, "DESCRIBE", uri)
		resp, err = rtspRequest(conn, r, "DESCRIBE", uri, 2, map[string]string{"Accept": "application/sdp", "Authorization": auth}, timeout)
		if err != nil {
			return RTSPResult{Err: err}
		}
	}
	res := RTSPResult{Code: resp.Code, Status: resp.Status}
	if resp.Code == 200 {
		res.Video = sdpVideoCodec(resp.Body)
	}
	return res
}

func sdpVideoCodec(sdp []byte) string {
	inVideo := false
	for _, line := range bytes.Split(sdp, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		if strings.HasPrefix(l, "m=") {
			inVideo = strings.HasPrefix(l, "m=video")
		}
		if inVideo && strings.HasPrefix(l, "a=rtpmap:") {
			if f := strings.Fields(l); len(f) >= 2 {
				return strings.SplitN(f[1], "/", 2)[0]
			}
		}
	}
	return ""
}

// ONVIF GetSystemDateAndTime — не требует пароля, показывает, что служба ONVIF (PTZ) жива.
func checkONVIF(ip string, port int, timeout time.Duration) (int, error) {
	body := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		`<s:Body><GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`
	url := fmt.Sprintf("http://%s/onvif/device_service", net.JoinHostPort(ip, strconv.Itoa(port)))
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Post(url, "application/soap+xml; charset=utf-8", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == 200 && !bytes.Contains(data, []byte("SystemDateAndTime")) {
		return resp.StatusCode, fmt.Errorf("ответ без данных ONVIF")
	}
	return resp.StatusCode, nil
}
