package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

type ModbusException struct{ Code byte }

func (e ModbusException) Error() string {
	names := map[byte]string{
		1:  "недопустимая функция (ПЛК не поддерживает чтение holding-регистров)",
		2:  "недопустимый адрес регистров (адрес/количество вне памяти ПЛК)",
		3:  "недопустимое значение в запросе",
		4:  "внутренняя ошибка ПЛК",
		6:  "ПЛК занят",
		10: "шлюз: путь недоступен",
		11: "шлюз: устройство не ответило (неверный Unit ID?)",
	}
	if n, ok := names[e.Code]; ok {
		return fmt.Sprintf("исключение Modbus %d — %s", e.Code, n)
	}
	return fmt.Sprintf("исключение Modbus %d", e.Code)
}

var modbusTID uint16

// Чтение holding-регистров (функция 03) по Modbus TCP.
func modbusReadHolding(conn net.Conn, unit byte, addr, count uint16, timeout time.Duration) ([]uint16, error) {
	modbusTID++
	tid := modbusTID
	req := make([]byte, 12)
	binary.BigEndian.PutUint16(req[0:], tid)
	binary.BigEndian.PutUint16(req[2:], 0)
	binary.BigEndian.PutUint16(req[4:], 6)
	req[6] = unit
	req[7] = 0x03
	binary.BigEndian.PutUint16(req[8:], addr)
	binary.BigEndian.PutUint16(req[10:], count)

	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	for {
		hdr := make([]byte, 7)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return nil, err
		}
		length := int(binary.BigEndian.Uint16(hdr[4:]))
		if length < 2 || length > 260 {
			return nil, fmt.Errorf("некорректный ответ (длина %d) — это точно Modbus TCP?", length)
		}
		body := make([]byte, length-1)
		if _, err := io.ReadFull(conn, body); err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint16(hdr[0:]) != tid {
			continue // старый ответ — пропускаем
		}
		if body[0] == 0x83 {
			if len(body) < 2 {
				return nil, errors.New("короткий ответ с исключением")
			}
			return nil, ModbusException{body[1]}
		}
		if body[0] != 0x03 || len(body) < 2 || int(body[1]) != int(count)*2 || len(body) < 2+int(count)*2 {
			return nil, fmt.Errorf("неожиданный ответ ПЛК: % X", body)
		}
		regs := make([]uint16, count)
		for i := range regs {
			regs[i] = binary.BigEndian.Uint16(body[2+i*2:])
		}
		return regs, nil
	}
}

type Telemetry struct {
	DistM     float64
	TempC     float64
	TempValid bool
	Flood     *bool
	AlarmCode uint16
	Mode      uint16
	Regs      []uint16
}

// Расшифровка регистров так же, как это делает ArmPipe.
func decodeTelemetry(p PLCSettings, regs []uint16, floodRaw *uint16) Telemetry {
	t := Telemetry{Regs: regs}
	get := func(i int) uint16 {
		if i < len(regs) {
			return regs[i]
		}
		return 0
	}
	dd, td := p.DistDiv, p.TempDiv
	if dd == 0 {
		dd = 1
	}
	if td == 0 {
		td = 1
	}
	if p.DistSigned {
		t.DistM = float64(int16(get(0))) / dd
	} else {
		t.DistM = float64(get(0)) / dd
	}
	t.TempC = float64(int16(get(1))) / td
	t.TempValid = t.TempC >= -40 && t.TempC <= 150
	if floodRaw != nil {
		v := (*floodRaw != 0) != p.FloodInvert
		t.Flood = &v
	}
	t.AlarmCode = get(3)
	t.Mode = get(4)
	return t
}

// Полное чтение: основной блок + датчик затопления (V-регистр = 512 + N).
func readTelemetry(conn net.Conn, p PLCSettings) (Telemetry, error) {
	regs, err := modbusReadHolding(conn, p.EffectiveUnit(), uint16(p.Start), uint16(p.Count), p.Timeout)
	if err != nil {
		return Telemetry{}, err
	}
	var flood *uint16
	if p.FloodEnabled {
		addr := 512 + p.FloodRegV
		if addr >= p.Start && addr < p.Start+len(regs) {
			v := regs[addr-p.Start]
			flood = &v
		} else if r, err := modbusReadHolding(conn, p.EffectiveUnit(), uint16(addr), 1, p.Timeout); err == nil {
			flood = &r[0]
		}
	}
	return decodeTelemetry(p, regs, flood), nil
}
