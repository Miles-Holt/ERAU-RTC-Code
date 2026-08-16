@echo off
REM Run this script from the controlnode\ directory.
REM On first run (internet-connected machine): downloads deps and vendors them.
REM On subsequent / airgap builds: vendors already present, just copies + builds.

echo [1/5] Downloading dependencies...
go mod download

echo [2/5] Vendoring dependencies for airgap builds...
go mod vendor

echo [3/5] Copying WebClient into static/ for embedding...
if exist static rmdir /S /Q static
xcopy /E /I /Y ..\WebClient static

echo [4/5] Refreshing the embedded protocol doc served at /docs/protocol...
copy /Y ..\docs\websocket-protocol.md webclient\embedded\websocket-protocol.md >nul

echo [5/5] Building controlnode.exe...
go build -mod=vendor -o controlnode.exe .

echo Done. controlnode.exe contains the embedded WebClient.
echo Copy controlnode.exe and the whole config\ directory (system.yaml, controlNode.yaml,
echo controls.yaml, daqNodes\, machines\, channels\, panelLayouts.yaml, userAuth.yaml and the
echo panel layout YAMLs) to the target machine, then run:  controlnode.exe -config-dir ..\config
