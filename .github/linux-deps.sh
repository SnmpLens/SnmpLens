#!/usr/bin/env bash
#
# The GTK and WebKit headers the Linux build needs, installed from the cache
# the calling job restored into $DEBS when it holds them, and from the Ubuntu
# mirror otherwise.
#
# The mirror is why this exists. The step took 21-41 s on an ordinary day and
# 172 s, 239 s, 1370 s and 1545 s on four others, with more runs cancelled
# after 15 minutes of a 25 MB package arriving at 50 KB/s. A cache hit does not
# touch the mirror at all.
#
# It is safe to replay the packages with dpkg because the cache key carries the
# runner IMAGE version: on one image the packages already installed are the
# same, so the debs apt chose for it install the same way. A new image is a new
# key and goes through apt. A replay that fails anyway falls back to apt rather
# than failing the job.
#
# The packages come from our own earlier run, which downloaded them through apt
# and its signature checks; dpkg does not check them again. That is acceptable
# for CI and is why release.yml does not use this: the job that builds the
# signed binaries installs from the mirror, verified, every time.
set -euo pipefail

PACKAGES=(libgtk-3-dev libwebkit2gtk-4.1-dev pkg-config)
DEBS="${APT_DEBS:-$HOME/apt-debs}"

if compgen -G "$DEBS/*.deb" > /dev/null; then
  echo "linux-deps: installing $(ls "$DEBS"/*.deb | wc -l) cached packages"
  if sudo dpkg -i "$DEBS"/*.deb; then
    exit 0
  fi
  echo "linux-deps: the cached packages did not install; falling back to apt" >&2
  sudo dpkg --configure -a || true
  sudo rm -rf "$DEBS"
fi

mkdir -p "$DEBS/partial"
sudo apt-get update
sudo apt-get install -y \
  -o Dir::Cache::Archives="$DEBS" \
  -o APT::Keep-Downloaded-Packages=true \
  "${PACKAGES[@]}"
# apt's own bookkeeping, owned by root and _apt, is not worth caching.
sudo rm -rf "$DEBS/partial" "$DEBS/lock"
echo "linux-deps: installed from the mirror; $(ls "$DEBS"/*.deb 2>/dev/null | wc -l) packages kept for the cache"
