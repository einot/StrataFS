#!/usr/bin/env bash
# Runs strata over a local directory standing in for a bucket, and prints the
# mount command. No credentials or cloud access required.
set -euo pipefail

root="${TMPDIR:-/tmp}/strata-demo"
bucket="$root/bucket"
mountpoint="$root/mnt"
port="${STRATA_PORT:-20490}"

mkdir -p "$bucket" "$mountpoint"

cd "$(dirname "$0")/.."
go build -o "$root/strata" ./cmd/strata

cat <<TXT
Bucket:      $bucket
Mount point: $mountpoint

Starting strata. In another terminal:

  sudo mount -t nfs -o vers=3,tcp,port=$port,mountport=$port,noresvport,nolocks,locallocks 127.0.0.1:/ $mountpoint

and when you are done:

  sudo umount $mountpoint

TXT

exec "$root/strata" -bucket "$bucket" -listen "127.0.0.1:$port" "$@"
