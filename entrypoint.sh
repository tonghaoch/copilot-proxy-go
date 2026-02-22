#!/bin/sh
set -e

# If first argument is --auth, run auth command
if [ "$1" = "--auth" ]; then
    shift
    exec copilot-proxy-go auth "$@"
fi

# Default: start the server with GH_TOKEN if provided
if [ -n "$GH_TOKEN" ]; then
    exec copilot-proxy-go start -g "$GH_TOKEN" "$@"
else
    exec copilot-proxy-go start "$@"
fi
