@echo off
REM Run this script from the controlnode\ directory.
REM Runs the Go smoke tests for the control node (broker, config, softchan,
REM webclient, and daqnode packages).  These exercise config parsing, the broker
REM fan-in/fan-out and command routing, software-channel validation, the
REM webclient WebSocket + static-file serving path, and the DAQ-node handshake.
REM
REM The webclient smoke test serves the sibling ..\WebClient directory, so run
REM from a checkout with WebClient\ present (it is skipped if missing).

echo Running control node smoke tests...
go test -mod=vendor ./...
if errorlevel 1 (
    echo.
    echo TESTS FAILED
    exit /b 1
)
echo.
echo All smoke tests passed.
