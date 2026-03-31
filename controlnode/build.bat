@echo off
REM Run this script from the controlnode\ directory.
REM On first run (internet-connected machine): downloads deps and vendors them.
REM On subsequent / airgap builds: vendors already present, just copies + builds.

echo [1/4] Downloading dependencies...
go mod download

echo [2/4] Vendoring dependencies for airgap builds...
go mod vendor

echo [3/4] Copying WebClient into static/ for embedding...
if exist static rmdir /S /Q static
xcopy /E /I /Y ..\WebClient static

echo [4/4] Building controlnode.exe...
go build -mod=vendor -o controlnode.exe .

echo Done. controlnode.exe contains the embedded WebClient — copy it and nodeConfigs_0.0.2.xml to the target machine.
