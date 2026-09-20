#!/usr/bin/env bash
# Exercises a live strata mount the way a real workload would: through the
# kernel's NFS client rather than through the test suite's RPC client.
#
#   ./scripts/smoke-test.sh /tmp/strata
#
# Must run in a shell that is allowed to touch network volumes. On macOS a
# sandboxed or non-interactive process is denied with EPERM regardless of the
# filesystem's own permissions.
set -uo pipefail

M="${1:-/tmp/strata}"
pass=0; fail=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }
head2(){ printf '\n\033[1m%s\033[0m\n' "$1"; }

# macOS resolves /tmp to /private/tmp, so compare physical paths.
real="$(cd "$M" 2>/dev/null && pwd -P)" || { echo "cannot enter $M" >&2; exit 1; }
if ! mount | grep -q " on $real "; then
  echo "nothing mounted at $M (resolved to $real)" >&2
  mount | grep -i nfs >&2
  exit 1
fi
echo "exercising strata at $M -> $real"

head2 "capacity"
df -h "$M" | tail -1

head2 "read path"
if [ -d "$M/documents" ]; then
  a=$(shasum -a 256 "$M/documents/large.txt"      | cut -d' ' -f1)
  b=$(shasum -a 256 "$M/documents/duplicate.txt"  | cut -d' ' -f1)
  c=$(shasum -a 256 "$M/documents/triplicate.txt" | cut -d' ' -f1)
  check "three deduplicated files read back identically" "$a=$b=$c" "$a=$a=$a"
else
  echo "  (no documents/ directory; skipping)"
fi

head2 "write path"
dd if=/dev/urandom of="$tmp/src.bin" bs=1m count=8 2>/dev/null
want=$(shasum -a 256 "$tmp/src.bin" | cut -d' ' -f1)
if cp "$tmp/src.bin" "$M/smoke-copied.bin"; then
  got=$(shasum -a 256 "$M/smoke-copied.bin" | cut -d' ' -f1)
  check "8 MB file survives a round trip through the kernel" "$got" "$want"
else
  bad "could not copy a file onto the mount"
fi

head2 "deduplication through the kernel"
cp "$tmp/src.bin" "$M/smoke-copied-2.bin" 2>/dev/null
got2=$(shasum -a 256 "$M/smoke-copied-2.bin" 2>/dev/null | cut -d' ' -f1)
check "a second identical copy reads back correctly" "$got2" "$want"

head2 "directories"
rm -rf "$M/smoke-dir" 2>/dev/null
if mkdir -p "$M/smoke-dir/nested/deep"; then ok "mkdir -p creates a nested path"; else bad "mkdir -p"; fi
echo "hello from strata" > "$M/smoke-dir/nested/deep/note.txt"
check "file written into a nested directory" "$(cat "$M/smoke-dir/nested/deep/note.txt" 2>/dev/null)" "hello from strata"
mv "$M/smoke-dir/nested/deep/note.txt" "$M/smoke-dir/moved.txt" 2>/dev/null
check "rename across directories" "$(cat "$M/smoke-dir/moved.txt" 2>/dev/null)" "hello from strata"
if rmdir "$M/smoke-dir" 2>/dev/null; then bad "rmdir removed a non-empty directory"; else ok "rmdir refuses a non-empty directory"; fi

head2 "random access"
printf 'aaaaaaaaaa' > "$M/smoke-edit.txt"
printf 'ZZZ' | dd of="$M/smoke-edit.txt" bs=1 seek=3 conv=notrunc 2>/dev/null
check "in-place overwrite at an offset" "$(cat "$M/smoke-edit.txt" 2>/dev/null)" "aaaZZZaaaa"
printf 'appended' >> "$M/smoke-edit.txt"
check "append extends the file" "$(cat "$M/smoke-edit.txt" 2>/dev/null)" "aaaZZZaaaaappended"

head2 "truncate"
dd if=/dev/zero of="$M/smoke-trunc.bin" bs=1k count=64 2>/dev/null
: > "$M/smoke-trunc.bin"
check "truncate to zero" "$(wc -c < "$M/smoke-trunc.bin" | tr -d ' ')" "0"

head2 "sparse files"
dd if=/dev/zero of="$M/smoke-sparse.bin" bs=1 count=1 seek=1048576 2>/dev/null
check "seek past the end grows the file" "$(wc -c < "$M/smoke-sparse.bin" | tr -d ' ')" "1048577"

head2 "symlinks"
ln -sf documents/large.txt "$M/smoke-link" 2>/dev/null
check "symlink target round-trips" "$(readlink "$M/smoke-link" 2>/dev/null)" "documents/large.txt"

head2 "metadata"
chmod 600 "$M/smoke-edit.txt" 2>/dev/null
check "chmod is persisted" "$(stat -f '%Sp' "$M/smoke-edit.txt" 2>/dev/null)" "-rw-------"

head2 "many files in one directory"
mkdir -p "$M/smoke-many"
for i in $(seq 1 200); do echo "$i" > "$M/smoke-many/f$i.txt"; done
check "200 files enumerate correctly" "$(ls "$M/smoke-many" | wc -l | tr -d ' ')" "200"
check "a file in the middle reads back" "$(cat "$M/smoke-many/f137.txt" 2>/dev/null)" "137"

head2 "cleanup"
rm -rf "$M"/smoke-* 2>/dev/null
if ls "$M"/smoke-* >/dev/null 2>&1; then bad "cleanup left files behind"; else ok "recursive delete removes everything"; fi

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
