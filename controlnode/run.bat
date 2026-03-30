@echo off
REM Run the control node.  Pass -config to override the XML path.
REM Default looks for nodeConfigs_0.0.2.xml one directory up.
controlnode.exe -config ..\nodeConfigs_0.0.2.xml
