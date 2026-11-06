#!/bin/sh
# Сборка ArmPipe-Diag.exe (можно собирать на Linux/macOS/Windows, нужен Go 1.21+).
# Иконка и сведения о версии лежат в rsrc_windows_amd64.syso (пересоздать: go-winres make --arch amd64).
set -e
cd "$(dirname "$0")"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../ArmPipe-Diag.exe .
echo "Готово: diag/ArmPipe-Diag.exe"
