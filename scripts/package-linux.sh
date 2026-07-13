#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: scripts/package-linux.sh <vX.Y.Z[-suffix]> [output-root]" >&2
  exit 2
fi

version="$1"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid release version: $version" >&2
  exit 2
fi

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_root="${2:-$repo/dist}"
commit="$(git -C "$repo" rev-parse HEAD)"
build_time="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
package_name="chainlab-$version-linux-amd64"
package_dir="$output_root/$package_name"
archive="$output_root/$package_name.tar.gz"
checksums="$output_root/$package_name-SHA256SUMS.txt"

if [[ -e "$package_dir" || -e "$archive" || -e "$checksums" ]]; then
  echo "release output already exists for $package_name" >&2
  exit 1
fi
mkdir -p "$package_dir"

cd "$repo"
go build -trimpath \
  -ldflags "-s -w -X chainlab/internal/buildinfo.Version=$version -X chainlab/internal/buildinfo.Commit=$commit -X chainlab/internal/buildinfo.BuildTime=$build_time" \
  -o "$package_dir/chainlab" ./cmd/chainlab
cp README.md README.zh-CN.md "$package_dir/"
cp docs/desktop-quick-start.md "$package_dir/QUICK-START.md"

version_output="$($package_dir/chainlab version)"
grep -Fq "\"version\": \"$version\"" <<<"$version_output"
grep -Fq "\"commit\": \"$commit\"" <<<"$version_output"
grep -Fq '"os": "linux"' <<<"$version_output"
grep -Fq '"arch": "amd64"' <<<"$version_output"

tar -C "$output_root" -czf "$archive" "$package_name"
{
  printf '%s  %s\n' "$(sha256sum "$package_dir/chainlab" | awk '{print $1}')" "$package_name/chainlab"
  printf '%s  %s\n' "$(sha256sum "$archive" | awk '{print $1}')" "$(basename "$archive")"
} >"$checksums"

printf '{"version":"%s","commit":"%s","package":"%s","archive":"%s","checksums":"%s"}\n' \
  "$version" "$commit" "$package_dir" "$archive" "$checksums"
