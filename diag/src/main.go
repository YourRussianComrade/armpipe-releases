// ArmPipe-Diag — проверка связи ПК с ПЛК (Modbus TCP) и камерами (RTSP/ONVIF)
// на машине санации и инспекции труб. Настройки берутся из конфигурации ArmPipe.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const appVersion = "1.0.0"

// ---------- вывод: цветной в консоль + чистый текст в отчёт ----------

type Out struct {
	color  bool
	report strings.Builder
}

const (
	cReset  = "\x1b[0m"
	cRed    = "\x1b[91m"
	cGreen  = "\x1b[92m"
	cYellow = "\x1b[93m"
	cCyan   = "\x1b[96m"
	cGray   = "\x1b[90m"
	cBold   = "\x1b[1m"
)

func (o *Out) paint(col, s string) string {
	if !o.color || col == "" {
		return s
	}
	return col + s + cReset
}

func (o *Out) raw(col, s string) {
	fmt.Println(o.paint(col, s))
	o.report.WriteString(s + "\r\n")
}

func (o *Out) Title(s string) {
	o.raw("", "")
	o.raw(cBold+cCyan, "■ "+s)
}
func (o *Out) OK(s string)   { o.mark(cGreen, "[ OK ]", s) }
func (o *Out) Fail(s string) { o.mark(cRed, "[НЕТ ]", s) }
func (o *Out) Warn(s string) { o.mark(cYellow, "[ !! ]", s) }
func (o *Out) Info(s string) { o.mark(cGray, "      ", s) }
func (o *Out) Hint(s string) { o.mark(cYellow, "   →  ", s) }

func (o *Out) mark(col, tag, s string) {
	fmt.Println(o.paint(col, tag) + " " + s)
	o.report.WriteString(tag + " " + s + "\r\n")
}

// ---------- итог по устройствам ----------

type Status int

const (
	StOK Status = iota
	StWarn
	StFail
)

type Summary struct {
	Name, Addr string
	St         Status
	Note       string
}

// ---------- проверка ПЛК ----------

func checkPLC(o *Out, p PLCSettings, adapters []Adapter) Summary {
	addr := net.JoinHostPort(p.IP, strconv.Itoa(p.Port))
	sum := Summary{Name: "ПЛК (Modbus TCP)", Addr: addr}
	o.Title(fmt.Sprintf("ПЛК  %s   (Unit ID %d, регистры %d…%d)", addr, p.EffectiveUnit(), p.Start, p.Start+p.Count-1))

	if net.ParseIP(p.IP) == nil {
		o.Fail("IP-адрес ПЛК не задан или некорректен: \"" + p.IP + "\"")
		o.Hint("Укажите IP ПЛК в ArmPipe → Настройки → ПЛК, или запустите: ArmPipe-Diag.exe -plc 192.168.x.x")
		sum.St, sum.Note = StFail, "IP не задан"
		return sum
	}

	// 1. Подсеть
	ifName, ifNet := adapterFor(adapters, p.IP)
	if ifName != "" {
		o.OK(fmt.Sprintf("ПЛК в одной подсети с сетевой картой «%s» (%s)", ifName, ifNet))
	} else {
		o.Warn("Ни одна сетевая карта ПК не настроена на подсеть ПЛК " + p.IP)
		o.Hint("Проверьте IP-адрес сетевой карты, к которой подключён ПЛК (Панель управления → Сетевые подключения → IPv4).")
		o.Hint("Адрес карты должен быть из той же подсети, например ПЛК 192.168.1.10 → ПК 192.168.1.100, маска 255.255.255.0.")
	}

	// 2. Ping
	ps := pingN(p.IP, 4, time.Second)
	pingOK := ps.Recv > 0
	switch {
	case ps.Recv == ps.Sent:
		o.OK(fmt.Sprintf("Ping: %d/%d ответов, среднее %s, макс %s", ps.Recv, ps.Sent, ms(ps.Avg), ms(ps.Max)))
	case ps.Recv > 0:
		o.Warn(fmt.Sprintf("Ping: потери пакетов — %d/%d ответов, среднее %s", ps.Recv, ps.Sent, ms(ps.Avg)))
		o.Hint("Нестабильная связь: проверьте разъёмы, кабель, коммутатор, наводки от силовых кабелей.")
	default:
		o.Fail(pingFailText(ps))
	}

	// 3. TCP-порт Modbus
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, p.Timeout)
	if err != nil {
		o.Fail(fmt.Sprintf("TCP-порт %d: %s", p.Port, describeNetErr(err)))
		switch {
		case isRefused(err):
			o.Hint("ПЛК отвечает, но Modbus TCP сервер на порту " + strconv.Itoa(p.Port) + " не принимает подключения.")
			o.Hint("Проверьте: включён ли Modbus TCP (slave) в ПЛК, верный ли порт, не исчерпан ли лимит подключений (закройте ArmPipe и повторите).")
			sum.Note = "порт Modbus закрыт"
		case !pingOK:
			o.Hint("Связи с ПЛК нет совсем: проверьте питание ПЛК, сетевой кабель и индикаторы LINK на ПЛК и сетевой карте ПК,")
			o.Hint("а также IP-адрес ПЛК и сетевой карты ПК (см. выше).")
			sum.Note = "нет связи"
		default:
			o.Hint("Ping проходит, но порт не отвечает — возможно, порт фильтруется или указан неверно.")
			sum.Note = "порт не отвечает"
		}
		sum.St = StFail
		return sum
	}
	defer conn.Close()
	o.OK(fmt.Sprintf("TCP-порт %d открыт (подключение за %s)", p.Port, ms(time.Since(t0))))
	if !pingOK {
		o.Info("(ping не проходит, но TCP работает — значит ICMP просто запрещён на ПЛК, это нормально)")
	}

	// 4. Чтение регистров
	var lat []time.Duration
	var lastT Telemetry
	var lastErr error
	for i := 0; i < 5; i++ {
		t1 := time.Now()
		t, err := readTelemetry(conn, p)
		if err != nil {
			lastErr = err
			if !isTimeout(err) {
				if _, ok := err.(ModbusException); !ok {
					break // соединение потеряно
				}
			}
			continue
		}
		lat = append(lat, time.Since(t1))
		lastT = t
		time.Sleep(100 * time.Millisecond)
	}
	if len(lat) == 0 {
		o.Fail("Чтение регистров Modbus: " + modbusErrText(lastErr))
		if _, ok := lastErr.(ModbusException); ok {
			o.Hint(fmt.Sprintf("ПЛК на связи, но отказал в чтении адресов %d…%d. Проверьте start_address/count в настройках ArmPipe и программу ПЛК.", p.Start, p.Start+p.Count-1))
		} else if isTimeout(lastErr) {
			o.Hint(fmt.Sprintf("ПЛК принял подключение, но не ответил на запрос. Проверьте Unit ID (сейчас %d) и что на порту %d именно Modbus TCP.", p.EffectiveUnit(), p.Port))
		} else {
			o.Hint("ПЛК разорвал соединение. Возможно, к ПЛК уже подключён ArmPipe или другая программа и лимит подключений исчерпан.")
		}
		sum.St, sum.Note = StFail, "Modbus не отвечает"
		return sum
	}
	var total, mx time.Duration
	for _, d := range lat {
		total += d
		if d > mx {
			mx = d
		}
	}
	avg := total / time.Duration(len(lat))
	if len(lat) < 5 {
		o.Warn(fmt.Sprintf("Чтение регистров: %d из 5 удачно, среднее %s (ошибка: %s)", len(lat), ms(avg), modbusErrText(lastErr)))
		sum.St, sum.Note = StWarn, "есть сбои чтения"
	} else {
		o.OK(fmt.Sprintf("Чтение регистров: 5 из 5, среднее %s, макс %s", ms(avg), ms(mx)))
	}
	printTelemetry(o, p, lastT)
	if sum.Note == "" {
		sum.Note = fmt.Sprintf("данные идут, отклик %s", ms(avg))
	}
	return sum
}

func modbusErrText(err error) string {
	if err == nil {
		return ""
	}
	if me, ok := err.(ModbusException); ok {
		return me.Error()
	}
	return describeNetErr(err)
}

func printTelemetry(o *Out, p PLCSettings, t Telemetry) {
	regs := make([]string, len(t.Regs))
	for i, r := range t.Regs {
		regs[i] = fmt.Sprintf("%d=%d", p.Start+i, r)
	}
	o.Info("Регистры: " + strings.Join(regs, "  "))
	temp := fmt.Sprintf("%.1f °C", t.TempC)
	if !t.TempValid {
		temp += " (вне диапазона — датчик не подключён?)"
	}
	o.Info(fmt.Sprintf("Дистанция: %.2f м   Температура: %s", t.DistM, temp))
	flood := "не опрашивается"
	if t.Flood != nil {
		flood = "нет"
		if *t.Flood {
			flood = "ЕСТЬ ВОДА"
		}
	}
	o.Info(fmt.Sprintf("Затопление: %s   Код аварии: %d   Режим: %d", flood, t.AlarmCode, t.Mode))
}

// ---------- проверка камер ----------

func checkCamera(o *Out, cam CameraSettings, adapters []Adapter) Summary {
	addr := net.JoinHostPort(cam.IP, strconv.Itoa(cam.RTSPPort))
	sum := Summary{Name: cam.Name, Addr: cam.IP}
	title := fmt.Sprintf("%s  %s", cam.Name, cam.IP)
	if cam.Disabled {
		title += "   (отключена в настройках ArmPipe)"
	}
	o.Title(title)

	if net.ParseIP(cam.IP) == nil {
		o.Fail("Некорректный IP-адрес камеры: \"" + cam.IP + "\"")
		sum.St, sum.Note = StFail, "неверный IP"
		return sum
	}
	if ifName, ifNet := adapterFor(adapters, cam.IP); ifName != "" {
		o.OK(fmt.Sprintf("Камера в одной подсети с сетевой картой «%s» (%s)", ifName, ifNet))
	} else {
		o.Warn("Ни одна сетевая карта ПК не настроена на подсеть камеры " + cam.IP)
	}

	ps := pingN(cam.IP, 4, time.Second)
	switch {
	case ps.Recv == ps.Sent:
		o.OK(fmt.Sprintf("Ping: %d/%d ответов, среднее %s, макс %s", ps.Recv, ps.Sent, ms(ps.Avg), ms(ps.Max)))
	case ps.Recv > 0:
		o.Warn(fmt.Sprintf("Ping: потери пакетов — %d/%d ответов, среднее %s", ps.Recv, ps.Sent, ms(ps.Avg)))
		o.Hint("Потери пакетов → видео будет дёргаться/зависать. Проверьте кабель, разъёмы, PoE-питание.")
	default:
		o.Fail(pingFailText(ps))
	}

	timeout := 4 * time.Second
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		o.Fail(fmt.Sprintf("RTSP-порт %d: %s", cam.RTSPPort, describeNetErr(err)))
		if ps.Recv == 0 {
			o.Hint("Камера недоступна: проверьте питание камеры (PoE/блок питания), кабель, IP-адрес камеры и сетевой карты.")
			sum.Note = "нет связи"
		} else {
			o.Hint("Камера отвечает на ping, но RTSP-порт закрыт. Проверьте порт RTSP и что RTSP включён в настройках камеры.")
			sum.Note = "RTSP-порт закрыт"
		}
		sum.St = StFail
		return sum
	}
	c.Close()
	o.OK(fmt.Sprintf("RTSP-порт %d открыт", cam.RTSPPort))

	paths := []struct{ label, path string }{{"Основной поток", cam.Path}}
	if cam.SubPath != "" && cam.SubPath != cam.Path {
		paths = append(paths, struct{ label, path string }{"Доп. поток", cam.SubPath})
	}
	worst := StOK
	for i, pp := range paths {
		r := checkRTSP(cam, pp.path, timeout)
		name := fmt.Sprintf("%s /%s", pp.label, strings.TrimLeft(pp.path, "/"))
		st, note := StOK, ""
		switch {
		case r.Err != nil:
			o.Fail(name + ": " + describeNetErr(r.Err))
			st, note = StFail, "RTSP не отвечает"
		case r.Code == 200:
			v := ""
			if r.Video != "" {
				v = " (видео " + r.Video + ")"
			}
			o.OK(name + ": поток доступен" + v)
		case r.Code == 401 || r.Code == 403:
			o.Fail(fmt.Sprintf("%s: доступ запрещён (%d) — неверный логин или пароль камеры", name, r.Code))
			if cam.User == "" {
				o.Hint("Логин камеры не задан в настройках ArmPipe.")
			} else {
				o.Hint("Проверьте логин «" + cam.User + "» и пароль камеры в настройках ArmPipe.")
			}
			st, note = StFail, "неверный логин/пароль"
		case r.Code == 404:
			o.Fail(fmt.Sprintf("%s: поток не найден (404) — неверный путь потока", name))
			o.Hint("Проверьте путь потока в настройках камеры в ArmPipe (по умолчанию stream1 / stream2).")
			st, note = StFail, "неверный путь потока"
		default:
			o.Warn(fmt.Sprintf("%s: камера ответила %d %s", name, r.Code, r.Status))
			st, note = StWarn, fmt.Sprintf("RTSP ответ %d", r.Code)
		}
		if i > 0 && st == StFail {
			// основной поток работает, не работает только дополнительный
			st, note = StWarn, "доп. поток: "+note
			o.Hint("ArmPipe по умолчанию показывает на экране именно доп. поток (display_stream = sub).")
		}
		if st > worst {
			worst, sum.Note = st, note
		}
	}

	if cam.ONVIFPort > 0 {
		code, err := checkONVIF(cam.IP, cam.ONVIFPort, timeout)
		switch {
		case err == nil && code == 200:
			o.OK(fmt.Sprintf("ONVIF (порт %d) отвечает — управление PTZ доступно", cam.ONVIFPort))
		case err != nil && code == 0:
			o.Warn(fmt.Sprintf("ONVIF (порт %d): %s — поворот/зум камеры работать не будет", cam.ONVIFPort, describeNetErr(err)))
			o.Hint("Проверьте порт ONVIF в настройках ArmPipe и что ONVIF включён в камере.")
			if worst < StWarn {
				worst, sum.Note = StWarn, "ONVIF/PTZ недоступен"
			}
		default:
			o.Warn(fmt.Sprintf("ONVIF (порт %d): ответ %d", cam.ONVIFPort, code))
			if worst < StWarn {
				worst, sum.Note = StWarn, "ONVIF отвечает с ошибкой"
			}
		}
	}

	sum.St = worst
	if sum.Note == "" {
		switch worst {
		case StOK:
			sum.Note = "видео доступно"
		case StFail:
			sum.Note = "видео недоступно"
		}
	}
	return sum
}

// ---------- общий прогон ----------

func runAll(o *Out, s *Settings) []Summary {
	o.raw(cBold, fmt.Sprintf("ArmPipe-Diag %s — проверка связи ПК ↔ ПЛК и камер", appVersion))
	o.raw(cGray, "Время: "+time.Now().Format("02.01.2006 15:04:05"))
	host, _ := os.Hostname()
	o.raw(cGray, "Компьютер: "+host)
	o.raw(cGray, "Настройки: "+s.Source)

	adapters := localAdapters()
	o.Title("Сетевые карты ПК")
	if len(adapters) == 0 {
		o.Fail("Не найдено ни одной сетевой карты с IPv4-адресом")
		o.Hint("Проверьте, что сетевой кабель подключён и адаптер включён.")
	}
	for _, a := range adapters {
		for _, n := range a.Nets {
			line := fmt.Sprintf("«%s»: %s  маска %s", a.Name, n.IP, maskString(n))
			if a.Up {
				o.OK(line)
			} else {
				o.Warn(line + "  (отключена)")
			}
		}
	}

	var res []Summary
	res = append(res, checkPLC(o, s.PLC, adapters))
	if len(s.Cameras) == 0 {
		o.Title("Камеры")
		o.Warn("В настройках нет ни одной камеры с IP-адресом — проверка камер пропущена")
	}
	for _, c := range s.Cameras {
		r := checkCamera(o, c, adapters)
		if c.Disabled {
			r.Note += " (отключена в ArmPipe)"
		}
		res = append(res, r)
	}

	o.raw("", "")
	o.raw(cBold, "════════════════════════ ИТОГ ════════════════════════")
	for _, r := range res {
		line := fmt.Sprintf("%-22s %-22s %s", r.Name, r.Addr, r.Note)
		switch r.St {
		case StOK:
			o.mark(cGreen, "[СВЯЗЬ ЕСТЬ]", line)
		case StWarn:
			o.mark(cYellow, "[ЕСТЬ СБОИ ]", line)
		default:
			o.mark(cRed, "[НЕТ СВЯЗИ ]", line)
		}
	}
	return res
}

// ---------- мониторинг ПЛК в реальном времени ----------

func monitorPLC(o *Out, p PLCSettings, in *bufio.Reader) {
	addr := net.JoinHostPort(p.IP, strconv.Itoa(p.Port))
	fmt.Println()
	fmt.Println(o.paint(cBold+cCyan, "■ Мониторинг ПЛК "+addr+" — опрос раз в 0,5 с. Нажмите Enter для выхода."))
	fmt.Println(o.paint(cGray, "  Можно шевелить кабель/разъёмы и смотреть, пропадает ли связь."))
	stop := make(chan struct{})
	go func() { in.ReadString('\n'); close(stop) }()

	var conn net.Conn
	var ok, fail, drops int
	wasOK := true
	defer func() {
		if conn != nil {
			conn.Close()
		}
		fmt.Printf("\nИтого: успешных опросов %d, ошибок %d, обрывов связи %d\n", ok, fail, drops)
	}()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		now := time.Now().Format("15:04:05")
		if conn == nil {
			c, err := net.DialTimeout("tcp", addr, p.Timeout)
			if err != nil {
				fail++
				if wasOK {
					drops++
				}
				wasOK = false
				fmt.Println(o.paint(cRed, fmt.Sprintf("%s  НЕТ СВЯЗИ  %s", now, describeNetErr(err))))
				continue
			}
			conn = c
		}
		t1 := time.Now()
		t, err := readTelemetry(conn, p)
		if err != nil {
			fail++
			if wasOK {
				drops++
			}
			wasOK = false
			fmt.Println(o.paint(cRed, fmt.Sprintf("%s  ОШИБКА     %s", now, modbusErrText(err))))
			conn.Close()
			conn = nil
			continue
		}
		ok++
		wasOK = true
		flood := "-"
		if t.Flood != nil {
			flood = "нет"
			if *t.Flood {
				flood = "ВОДА"
			}
		}
		fmt.Printf("%s  %s  %4d мс  дист %7.2f м  темп %6.1f °C  вода %-4s  авария %d  режим %d\n",
			now, o.paint(cGreen, "OK"), time.Since(t1).Milliseconds(), t.DistM, t.TempC, flood, t.AlarmCode, t.Mode)
	}
}

// ---------- сохранение отчёта ----------

func saveReport(text string) (string, error) {
	name := "ArmPipe-Diag_" + time.Now().Format("2006-01-02_15-04-05") + ".txt"
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "reports"))
	}
	dirs = append(dirs, filepath.Join(filepath.Dir(armpipeConfigPath()), "diag"))
	var lastErr error
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			lastErr = err
			continue
		}
		path := filepath.Join(d, name)
		if err := os.WriteFile(path, []byte("\uFEFF"+text), 0o644); err != nil {
			lastErr = err
			continue
		}
		return path, nil
	}
	return "", lastErr
}

// ---------- main ----------

var stdinClosed bool

func ask(in *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	s, err := in.ReadString('\n')
	if err != nil && s == "" {
		stdinClosed = true
	}
	return strings.TrimSpace(s)
}

func main() {
	cfgPath := flag.String("config", "", "путь к config.json ArmPipe (по умолчанию %LOCALAPPDATA%\\ArmPipe\\config.json)")
	plcIP := flag.String("plc", "", "IP-адрес ПЛК (переопределяет настройки ArmPipe)")
	plcPort := flag.Int("port", 0, "порт Modbus TCP ПЛК (по умолчанию 502)")
	camIP := flag.String("cam", "", "IP-адрес камеры (переопределяет основную камеру)")
	once := flag.Bool("once", false, "одна проверка без меню (для запуска из скриптов)")
	monitor := flag.Bool("monitor", false, "сразу открыть мониторинг ПЛК")
	flag.Parse()

	o := &Out{color: setupConsole("ArmPipe-Diag — проверка связи ПЛК и камер")}
	in := bufio.NewReader(os.Stdin)

	path := *cfgPath
	if path == "" {
		path = armpipeConfigPath()
	}
	s, err := loadArmPipeConfig(path)
	if err != nil {
		fmt.Println(o.paint(cYellow, "Настройки ArmPipe не найдены ("+path+"): "+err.Error()))
		s = &Settings{Source: "введены вручную", PLC: defaultPLC()}
		if *plcIP == "" && !*once {
			*plcIP = ask(in, "Введите IP-адрес ПЛК: ")
		}
		if *camIP == "" && !*once {
			*camIP = ask(in, "Введите IP-адрес камеры (Enter — пропустить): ")
		}
	}
	if *plcIP != "" {
		s.PLC.IP = *plcIP
		s.Source += " (+ IP ПЛК из командной строки)"
	}
	if *plcPort > 0 {
		s.PLC.Port = *plcPort
	}
	if *camIP != "" {
		cam := CameraSettings{Name: "Камера 1 (PTZ)", IP: *camIP, RTSPPort: 554, Path: "stream1", SubPath: "stream2", ONVIFPort: 2020}
		if len(s.Cameras) > 0 {
			s.Cameras[0].IP = *camIP
		} else {
			s.Cameras = append(s.Cameras, cam)
		}
	}

	if *monitor {
		monitorPLC(o, s.PLC, in)
		return
	}

	for {
		o.report.Reset()
		res := runAll(o, s)
		if p, err := saveReport(o.report.String()); err == nil {
			fmt.Println(o.paint(cGray, "\nОтчёт сохранён: "+p))
		}
		if *once {
			for _, r := range res {
				if r.St == StFail {
					os.Exit(1)
				}
			}
			return
		}
		fmt.Println()
		fmt.Println(o.paint(cBold, "Enter — проверить ещё раз   M — мониторинг ПЛК в реальном времени   Q — выход"))
		choice := strings.ToLower(ask(in, "> "))
		if stdinClosed {
			return
		}
		switch choice {
		case "q", "й", "exit", "выход":
			return
		case "m", "ь", "м":
			monitorPLC(o, s.PLC, in)
			fmt.Println()
			if strings.ToLower(ask(in, "Enter — полная проверка, Q — выход: ")) == "q" {
				return
			}
		}
	}
}

func pingFailText(ps PingStats) string {
	t := fmt.Sprintf("Ping: нет ответа (0/%d)", ps.Sent)
	if ps.LastErr != nil && ps.LastErr.Error() != "нет ответа" {
		t += " — " + ps.LastErr.Error()
	}
	return t
}
