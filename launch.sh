#!/bin/bash
# LeoStudio launcher — starts HTTP server for MCP, then launches desktop app
systemctl --user start leostudio-server 2>/dev/null
systemctl --user start leostudio-import 2>/dev/null
sleep 1
exec /home/angkolj/HERMES_WORKSPACE/leostudio/leostudio-linux-amd64 "$@"
