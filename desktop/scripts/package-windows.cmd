@echo off
rem 双击即可本地打包 GCMS Pilot（Windows）。详见 package-windows.ps1 头部说明。
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0package-windows.ps1"
pause
