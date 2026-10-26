# 构建 NaiLink

## 环境与源码

使用 Linux 或 WSL：Node.js 22、pnpm 10.24.0、Go 1.26.0、Python 3、Make、Clang / LLVM 15+、Git。

```sh
git clone --recurse-submodules https://github.com/slobys/NaiLink.git
cd NaiLink
corepack enable
corepack prepare pnpm@10.24.0 --activate
pnpm install --frozen-lockfile
```

GitHub 的 Download ZIP 不包含后端子模块。已有工作区运行 `git submodule update --init --recursive`，不要用强制重置脚本清除本地修改。

## OpenWrt 插件

遵循 [OpenWrt 开发说明](../openwrt/README.md#开发)，构建 LuCI 专用前端、后端增量及原生 IPK / APK。MIPS 的 SQLite 需要 SDK 中的 CGO 静态工具链，不能套用纯 Go 构建。

## 普通 Linux 程序

```sh
pnpm build --filter=daed
python3 openwrt/prepare-wing.py wing
make VERSION=0.6.33
./daed --version
sudo ./daed run --config /etc/daed
```

普通 Linux 使用独立前端和初始化登录；LuCI 专用前端只适用于 OpenWrt 集成。准备步骤不可省略，否则会遗漏本仓库的后端修复。构建前核对版本；若工具使用版本后缀，设置 `CLANG=clang-15` 并保证 `llvm-strip` 可用。

可选 systemd 服务（仅普通 Linux）：

```sh
sudo install -Dm755 daed /usr/bin/daed
sudo install -Dm644 install/daed.service /etc/systemd/system/daed.service
sudo systemctl daemon-reload
sudo systemctl enable --now daed
```

运行需要 GeoIP / GeoSite 规则库，将可信版本放到 `/usr/share/daed/geoip.dat` 和 `/usr/share/daed/geosite.dat`。Docker 已固定规则快照和 SHA-256。保留 `/etc/daed`，不要让容器与 systemd 实例同时接管网络。

## Docker

```sh
docker build --build-arg DAED_VERSION=0.6.33 -t nailink:local .
docker run --rm nailink:local daed --version
docker compose up -d
```

默认使用当前主机平台，支持 `linux/amd64` / `linux/arm64`。跨架构可用 `docker buildx build --platform linux/arm64 --load -t nailink:arm64 .`。运行镜像不包含开发依赖、仓库历史或本地配置。

镜像用于独立 Linux 部署，透明代理仍要求宿主机内核能力和网络权限，见[安装说明](getting-started.md#普通-linux--docker)。

## 检查与发布

```sh
pnpm test
pnpm check-types
python3 -m unittest discover -s tests/repository -v
python3 -m unittest discover -s tests/docs -v
python3 docs/site/build.py
```

发布入口见[贡献指南](../CONTRIBUTING.md#构建与发布)，验证边界见 [OpenWrt 验证记录](../openwrt/VALIDATION.md)。

修改 GraphQL schema 可用 `SCHEMA_PATH=/path/to/schema.graphql pnpm codegen`；不要把生产凭据写进配置。
