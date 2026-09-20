#!/usr/bin/env bash
# Runs strata over two local directories and prints the mount command.
# No credentials or cloud access required.
set -euo pipefail

root="${TMPDIR:-/tmp}/strata-demo"
data="$root/data"
meta="$root/meta"
mountpoint="$root/mnt"
port="${STRATA_PORT:-20490}"

mkdir -p "$data" "$meta" "$mountpoint"

cd "$(dirname "$0")/.."
go build -o "$root/strata" ./cmd/strata

cat <<TXT
Data bucket:     $data
Metadata bucket: $meta
Mount point:     $mountpoint

Starting strata. In another terminal:

  sudo mount -t nfs -o vers=3,tcp,port=$port,mountport=$port,noresvport 127.0.0.1:/ $mountpoint

and when you are done:

  sudo umount $mountpoint

TXT

exec "$root/strata" -data "$data" -meta "$meta" -listen "127.0.0.1:$port" "$@"
