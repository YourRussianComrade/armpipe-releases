package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Настройки ПЛК — те же ключи и значения по умолчанию, что в ArmPipe (секция "plc" и "flood").
type PLCSettings struct {
	IP         string
	Port       int
	UnitID     int
	UseUnitID  bool
	Start      int
	Count      int
	Timeout    time.Duration
	DistDiv    float64
	TempDiv    float64
	DistSigned bool

	FloodEnabled bool
	FloodRegV    int
	FloodInvert  bool
}

// Unit ID, который реально уходит в запросе. ArmPipe (pymodbus) без use_unit_id шлёт 1.
func (p PLCSettings) EffectiveUnit() byte {
	if p.UseUnitID {
		return byte(p.UnitID)
	}
	return 1
}

type CameraSettings struct {
	Name      string
	IP        string
	User      string
	Password  string
	RTSPPort  int
	Path      string
	SubPath   string
	ONVIFPort int // 0 — не проверять
	Disabled  bool
}

type Settings struct {
	Source  string // откуда взяты настройки
	PLC     PLCSettings
	Cameras []CameraSettings
}

func defaultPLC() PLCSettings {
	return PLCSettings{
		Port: 502, UnitID: 1, Start: 612, Count: 5,
		Timeout: 3 * time.Second, DistDiv: 83.4, TempDiv: 10, DistSigned: true,
		FloodEnabled: true, FloodRegV: 102,
	}
}

func armpipeConfigPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "ArmPipe", "config.json")
}

// ---- чтение JSON без жёсткой схемы ----

type jmap map[string]any

func (m jmap) sub(key string) jmap {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return jmap{}
}

func (m jmap) str(key, def string) string {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return def
}

func (m jmap) num(key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

func (m jmap) integer(key string, def int) int { return int(m.num(key, float64(def))) }

func (m jmap) boolean(key string, def bool) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	}
	return def
}

func loadArmPipeConfig(path string) (*Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimPrefix(string(raw), "\uFEFF"))
	var root jmap
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("файл повреждён: %v", err)
	}
	s := &Settings{Source: path, PLC: defaultPLC()}

	p := root.sub("plc")
	d := defaultPLC()
	s.PLC = PLCSettings{
		IP:         p.str("ip", ""),
		Port:       p.integer("port", d.Port),
		UnitID:     p.integer("unit_id", d.UnitID),
		UseUnitID:  p.boolean("use_unit_id", false),
		Start:      p.integer("start_address", d.Start),
		Count:      p.integer("count", d.Count),
		Timeout:    time.Duration(p.num("timeout_s", 3) * float64(time.Second)),
		DistDiv:    p.num("distance_divider", d.DistDiv),
		TempDiv:    p.num("temperature_divider", d.TempDiv),
		DistSigned: p.boolean("distance_signed", true),
	}
	f := root.sub("flood")
	s.PLC.FloodEnabled = f.boolean("enabled", true)
	s.PLC.FloodRegV = f.integer("register_v", d.FloodRegV)
	s.PLC.FloodInvert = f.boolean("invert", false)
	if s.PLC.Timeout <= 0 {
		s.PLC.Timeout = 3 * time.Second
	}
	if s.PLC.Count < 1 {
		s.PLC.Count = d.Count
	}

	c := root.sub("camera")
	if ip := c.str("ip", ""); ip != "" {
		s.Cameras = append(s.Cameras, CameraSettings{
			Name:      "Камера 1 (PTZ)",
			IP:        ip,
			User:      c.str("user", ""),
			Password:  c.str("password", ""),
			RTSPPort:  c.integer("rtsp_port", 554),
			Path:      c.str("main_path", "stream1"),
			SubPath:   c.str("sub_path", "stream2"),
			ONVIFPort: c.integer("onvif_port", 2020),
		})
	}
	if extras, ok := root["extra_cameras"].([]any); ok {
		for i, e := range extras {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			x := jmap(em)
			ip := x.str("ip", "")
			if ip == "" {
				continue
			}
			name := x.str("name", "")
			if name == "" {
				name = fmt.Sprintf("Камера %d", i+2)
			}
			s.Cameras = append(s.Cameras, CameraSettings{
				Name:      name,
				IP:        ip,
				User:      x.str("user", ""),
				Password:  x.str("password", ""),
				RTSPPort:  x.integer("rtsp_port", 554),
				Path:      x.str("path", "stream1"),
				SubPath:   x.str("sub_path", ""),
				ONVIFPort: x.integer("onvif_port", 0),
				Disabled:  !x.boolean("enabled", true),
			})
		}
	}
	return s, nil
}
