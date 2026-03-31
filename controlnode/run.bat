@echo off
REM Run the control node with live WebClient files from disk (dev mode).
REM Use -webroot to serve the WebClient directory directly so HTML/JS/CSS changes
REM take effect on browser refresh without rebuilding the exe.
REM Omit -webroot (or deploy just the exe) to use the embedded WebClient instead.
controlnode.exe -config ..\nodeConfigs_0.0.2.xml -webroot ..\WebClient
