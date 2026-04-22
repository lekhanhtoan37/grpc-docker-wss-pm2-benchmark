#!/bin/bash
exec "$(ls -d "$HOME/.nvm/versions/node/v20"*/bin/node 2>/dev/null | head -1 || echo node)" --max-old-space-size=16384 "$(dirname "$0")/server.js"
