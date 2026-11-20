@rem Builds the local apitool.exe: the GUI subsystem (no console window) plus
@rem the icon and version resources from viva.toml.
@echo off
go build -trimpath -ldflags "-H=windowsgui" -o apitool.exe . || exit /b 1
go run ./tools/patchicon
