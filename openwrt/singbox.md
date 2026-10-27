# 可选 sing-box 兼容构建

版本：`1.14.3-nailink.1`。上游：[SagerNet/sing-box v1.14.3](https://github.com/SagerNet/sing-box/tree/v1.14.3)，保留上游 GPL 许可证。它是按需下载的静态程序，不安装上游服务，不改写用户节点或配置。

## 为什么提供兼容构建

在 ARM64 OpenWrt 真机上，同一条 Reality + Vision 节点使用 dae 可联网，官方 sing-box 1.14.3、1.14.4 的独立连接出现 Reality 验证失败。核对链接参数和服务器解析地址后，独立对照验证定位到握手状态创建后移除 ML-KEM 密钥份额的路径。

Reality 补丁保留原 ClientHello 的密钥份额与生成的私钥配对，并在普通 ECDHE 私钥为空时读取混合密钥份额中的 ECDHE 私钥。未修改服务器、公钥、短 ID、SNI、指纹、密码、客户端版本号或验证条件。错误短 ID 和证书仍必须拒绝。转换器也不会为连通而关闭认证或切换到直连。

另一处适配保留 dae 的 `dial_mode: domain`：只在代理规则已按原 IP/域名匹配之后，使用嗅探到的有效域名拨号，避免旧的错误 DNS 地址导致切换后必须清缓存。直连规则、显式覆盖地址和 `ip` 模式保持原行为；手动测试继续使用指定检测 IP。`domain+` 等尚不能等价转换的模式明确阻止切换。

补丁实现位于 [singbox.py](singbox.py) 的 `patch_reality` 和 `patch_domain_route`，输入上游源码先校验 SHA256，不接受未知实现；构建时运行对应的原生 Go 回归。

## 构建和校验

Linux 上使用 **Go 1.26.0** 与 Python 3.12+：

```sh
python3 openwrt/singbox.py --cpu x86_64 --work /tmp/nailink-core-amd64 --output .build/optional-core --verify-pins
python3 openwrt/singbox.py --cpu arm64 --work /tmp/nailink-core-arm64 --output .build/optional-core --verify-pins
```

工作目录必须是新的目录，脚本拒绝覆盖已有源码。构建使用固定源码归档、Go 版本、构建标签和静态交叉编译，去除本机路径和构建 ID，归档统一排序、所有者和时间。`--verify-pins` 检查结果与安装器固定的 SHA256 一致，不一致则拒绝发布。

每次发布保留两种 CPU 的程序归档、`nailink-sing-box-1.14.3-nailink.1-source.tar.gz` 对应源码以及 `SHA256SUMS`。源码归档包含补丁后的完整上游项目、许可证、构建脚本、来源及原始源码校验值；依赖由原项目的 `go.mod` / `go.sum` 锁定。归档虽沿用可安全提取的双层数据布局，但不是要安装到系统的 IPK 包。

## 验证边界

保留独立官方服务端，对兼容构建测试 SS/SS2022、VMess、VLESS TLS/Reality/Vision、Trojan、HY2、AnyTLS、TUIC 的 HTTP、负载、分段传输和 UDP；验证错误证书、错误短 ID 被拒绝。透明代理、双栈 DNS、AI 分流、双向切换及失败恢复由真实 BPF/TUN/nftables 隔离测试和 ARM64 路由器现场另行检查。它不代表兼容所有服务端扩展，未适配的参数继续阻止切换。

## 可选扩展包

0.6.39 另提供 `1.14.3-nailink.2` 扩展包，保留以上兼容补丁，增加 `with_naive_outbound,with_musl,with_wireguard`。Naive 使用上游固定版本 Cronet 静态库；LLD 20.1.2 必须处理其 CREL 重定位，旧链接器即使构建成功也可能在真正初始化 Cronet 时崩溃。普通轻量内核仍保留 `nailink.1`，不强制增加所有用户的体积和内存。

```sh
python3 openwrt/extra.py --cpu x86_64 --work /tmp/nailink-extra-x64 --sdk-work /tmp/nailink-sdk --output .build/extra-core --verify-pins
python3 openwrt/extra.py --cpu arm64 --work /tmp/nailink-extra-arm64 --sdk-work /tmp/nailink-sdk --output .build/extra-core --verify-pins
```

需要 Go 1.26.0、LLD 20.1.2、zstd 和脚本校验的 OpenWrt 24.10.4 SDK（SDK 自动下载）。Tor 0.4.9.13 及库来自固定 SHA256 的 OpenWrt 官方包，只提取程序、依赖和 GeoIP，不执行安装脚本。静态启动器设置父进程退出信号，并只加载私有库；Tor 原生实现中输出临时代理认证信息的两条 Trace 日志已移除。

输入记录见 `extra-sources.json`。归档提供每个文件的摘要、完整许可证、Go 模块声明、Tor 等外部组件的固定源码链接；另附扩展核心的完整源码和构建脚本。安装前校验整个固定归档，再逐文件验证，拒绝链接、越界路径、重复项和不完整包。功能、资源和架构边界见[使用说明](../docs/core-switching.md)。
