// SPDX-License-Identifier: AGPL-3.0-only
package routercore

type ProtocolCapability struct {
	Name      string `json:"name"`
	Formats   string `json:"formats"`
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}
type EngineCapability struct {
	ID        string               `json:"id"`
	Version   string               `json:"version"`
	Protocols []ProtocolCapability `json:"protocols"`
}

func ProtocolCapabilities() []EngineCapability {
	common := []ProtocolCapability{
		{"Shadowsocks / SS2022", "ss:// / JSON", true, ""}, {"VMess", "vmess:// / JSON", true, "AEAD，alterId=0"},
		{"VLESS / REALITY", "vless:// / JSON", true, ""}, {"Trojan", "trojan:// / JSON", true, ""},
		{"Hysteria2", "hy2://、hysteria2:// / JSON", true, ""}, {"TUIC v5", "tuic:// / JSON", true, ""},
		{"AnyTLS", "anytls:// / JSON", true, ""}, {"SOCKS5", "socks5:// / JSON", true, ""}, {"HTTP / HTTPS", "http://、https:// / JSON", true, ""},
	}
	dae := append([]ProtocolCapability{}, common...)
	for i := range dae {
		dae[i].Formats = "普通节点链接"
	}
	dae = append(dae, ProtocolCapability{"ShadowsocksR", "ssr://", true, ""}, ProtocolCapability{"Juicity", "juicity://", true, ""}, ProtocolCapability{"Naive HTTPS", "naive+https://", true, "内置实现"}, ProtocolCapability{"ShadowTLS v3", "SS 插件参数", true, "配合 Shadowsocks 使用"})
	sb := append([]ProtocolCapability{}, common...)
	sb = append(sb, ProtocolCapability{"Hysteria 1", "hysteria:// / JSON", true, "需正确填写带宽及认证参数"}, ProtocolCapability{"SSH", "ssh:// / JSON", true, "支持内联密钥及主机公钥"}, ProtocolCapability{"Snell v4 / v6", "snell:// / JSON", true, "其他版本不支持"}, ProtocolCapability{"ShadowTLS", "JSON 出站组合", true, "与 SS 等内层代理组合使用"}, ProtocolCapability{"Naive HTTPS", "naive+https:// / JSON", true, "需要扩展协议包；Cronet TLS，不支持跳过证书验证"}, ProtocolCapability{"WireGuard", "wireguard:// / endpoint JSON", true, "需要扩展协议包；用户态 endpoint，默认 1 个线程"}, ProtocolCapability{"Tor", "tor://default / JSON", true, "需要扩展协议包；仅 TCP，首次引导较慢，每个运行配置最多 1 个"}, ProtocolCapability{"ShadowsocksR / Juicity", "", false, "该 sing-box 版本不实现这些出站协议"})
	return []EngineCapability{{ID: "dae", Version: "内置", Protocols: dae}, {ID: "sing-box", Version: SingBoxVersion, Protocols: sb}}
}
