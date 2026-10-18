#!/usr/bin/env bash
# Mounts a running strata server. Needs root, because mount(8) does.
set -euo pipefail

port="${1:-20490}"
mountpoint="${2:-/tmp/strata}"

mkdir -p "$mountpoint"

# strata serves NFS but not the separate NLM lock protocol, so locking stays
# local. The option is spelled differently on each platform.
# nosuid,nodev: any local user can reach the server as any uid, and if the mount
# outlives strata any local user can bind its unprivileged port and serve a
# set-id or device-node file (README, Honest limitations).
opts="vers=3,tcp,port=$port,mountport=$port,noresvport,hard,nosuid,nodev"
if [[ "$(uname -s)" == "Darwin" ]]; then
  opts="$opts,nolocks,locallocks"
else
  opts="$opts,nolock"
fi

echo "mounting 127.0.0.1:/ on $mountpoint"
exec sudo mount -t nfs -o "$opts" 127.0.0.1:/ "$mountpoint"
