# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS build-web
WORKDIR /src
ENV HUSKY=0 CI=true
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml turbo.json tsconfig.json ./
COPY apps ./apps
COPY packages ./packages
RUN corepack enable && corepack prepare pnpm@10.24.0 --activate
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile && pnpm build --filter=daed

FROM --platform=$BUILDPLATFORM golang:1.26.0-bookworm AS build-bundle
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates clang-15 git llvm-15 make python3 \
    && ln -s /usr/bin/llvm-strip-15 /usr/local/bin/llvm-strip \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY wing ./wing
COPY openwrt/prepare-wing.py ./openwrt/prepare-wing.py
COPY openwrt/wing ./openwrt/wing
COPY openwrt/corelib ./openwrt/corelib
COPY --from=build-web /src/apps/web/dist ./web
ARG TARGETARCH
ARG DAED_VERSION=source
ENV CGO_ENABLED=0 CLANG=clang-15
# Normalize only the copied text sources from Windows checkouts; hashes stay enforced.
RUN test -f wing/go.mod && test -f wing/dae-core/control/kern/tproxy.c \
    && find wing -type f \( -name '*.go' -o -name '*.mod' -o -name '*.sum' -o -name '*.mk' -o -name '*.c' -o -name '*.h' -o -name Makefile -o -name .gitmodules \) -exec sed -i 's/\r$//' {} + \
    && python3 openwrt/prepare-wing.py wing
# The upstream generated make fragment repeats assignments and cannot bootstrap
# without .git. Use the already populated, pinned header directories instead.
RUN python3 - <<'PY'
import configparser
import os
from pathlib import Path
root = Path('/src/wing/dae-core')
modules = configparser.ConfigParser()
modules.read(root / '.gitmodules')
paths = [modules[section]['path'] for section in modules.sections()]
for name in paths:
    if not (root / name).is_dir() or not any((root / name).iterdir()):
        raise SystemExit('Missing nested submodule: ' + name + '; clone --recurse-submodules')
(root / '.gitmodules.d.mk').write_text('submodule_paths=' + ' '.join(paths) + '\n')
for name in paths:
    os.utime(root / name, None)
PY
WORKDIR /src/wing
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    case "$TARGETARCH" in amd64|arm64) ;; *) echo 'Docker supports linux/amd64 and linux/arm64 only' >&2; exit 1;; esac \
    && GOOS=linux GOARCH="$TARGETARCH" make APPNAME=daed VERSION="$DAED_VERSION" \
       OUTPUT=/out/daed WEB_DIST=/src/web bundle

# Fixed upstream snapshot and checksums; never silently use changing master files.
FROM scratch AS geodata
ADD --checksum=sha256:116cc0f03d48991962f7f9cdbbcf53d45d89777f9c3cd1a663210853e5df6093 https://raw.githubusercontent.com/v2rayA/dist-v2ray-rules-dat/1bed4e52832516f59ee3f0fa27f5255f68fd0e75/geoip.dat /geoip.dat
ADD --checksum=sha256:c5a73fbd415bb0506ecf8fa574caadc6f54a71d9308c265e97da49e4cebf8466 https://raw.githubusercontent.com/v2rayA/dist-v2ray-rules-dat/1bed4e52832516f59ee3f0fa27f5255f68fd0e75/geosite.dat /geosite.dat

FROM debian:bookworm-slim AS prod
LABEL org.opencontainers.image.title="NaiLink" \
      org.opencontainers.image.source="https://github.com/slobys/NaiLink" \
      org.opencontainers.image.licenses="MIT AND AGPL-3.0-only"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates iproute2 \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /etc/daed /usr/share/daed
COPY --from=build-bundle /out/daed /usr/local/bin/daed
COPY --from=geodata /geoip.dat /geosite.dat /usr/share/daed/
EXPOSE 2023
CMD ["daed", "run", "-c", "/etc/daed"]
