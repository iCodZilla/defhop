@echo off
REM Open /root/main on 10.129.200.66 inside VS Code Remote-SSH (not local Windows).
REM Use VS Code (code), not Cursor.
"D:\applications\vs_code\bin\code.cmd" --folder-uri "vscode-remote://ssh-remote+10.129.200.66/root/main"
