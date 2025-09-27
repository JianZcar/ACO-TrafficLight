#!/bin/bash
set -e

Xvfb :1 -screen 0 1920x1080x16 &
export DISPLAY=:1

x11vnc -display :1 -nopw -forever -shared -quiet &
websockify --web=/usr/share/novnc/ 8080 localhost:5900 >/dev/null 2>&1 &
exec python -u /server/main.py
