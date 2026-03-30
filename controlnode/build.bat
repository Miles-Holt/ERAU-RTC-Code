@echo off
REM Run this script once after cloning on a machine with internet access (or
REM a machine that has already downloaded the Go module cache).
REM After running, the vendor/ folder contains all dependencies and the binary
REM can be built on the airgapped LAN with no network access.

echo [1/3] Downloading dependencies...
go mod download

echo [2/3] Vendoring dependencies for airgap builds...
go mod vendor

echo [3/3] Building controlnode.exe...
go build -mod=vendor -o controlnode.exe .

echo Done. Copy controlnode.exe and nodeConfigs_0.0.2.xml to the target machine.
