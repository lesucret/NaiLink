// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

type optionProblem struct {
	code   string
	fields []string
}

func (e *optionProblem) Error() string { return e.code }

var safeOptionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

func OptionProblem(code string, fields ...string) error {
	clean := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		if !safeOptionName.MatchString(field) {
			field = "异常参数名"
		}
		if !seen[field] {
			clean = append(clean, field)
			seen[field] = true
		}
	}
	sort.Strings(clean)
	return &optionProblem{code, clean}
}

// Reports include parameter names only, never credentials, URLs or values.
func DescribeError(err error) string {
	message := ProblemText(err.Error())
	var option *optionProblem
	if errors.As(err, &option) && len(option.fields) > 0 {
		message += "（参数：" + strings.Join(option.fields, "、") + "）"
	}
	return message
}
func ProblemText(code string) string {
	if message, ok := problemMessages[code]; ok {
		return message
	}
	if strings.HasPrefix(code, "unsupported_rule_") {
		return "当前路由匹配方式尚未适配，原规则保留。"
	}
	return "配置暂未通过兼容性检查，请核对协议、传输方式和附加参数；原配置保留。"
}

var problemMessages = map[string]string{
	"unsupported_dial_mode":               "当前拨号模式尚未适配，仅支持按域名或按 IP 拨号；原设置保留",
	"unsupported_node_option":             "节点包含尚未支持或重复的参数",
	"unsupported_header_type":             "当前伪装头类型尚未适配，仅支持无额外伪装头",
	"invalid_udp_option":                  "UDP 开关必须为 0、1、true 或 false",
	"invalid_spider_path":                 "REALITY 爬虫路径格式异常，或被用于非 REALITY 节点",
	"invalid_node":                        "节点链接、认证信息、地址或端口格式异常",
	"invalid_node_name":                   "节点缺少可用名称",
	"conflicting_node_options":            "同一设置的别名参数互相冲突",
	"invalid_tls_option":                  "证书校验开关格式异常",
	"unsupported_node_path":               "当前协议不支持链接中的额外路径",
	"unsupported_ss_plugin":               "Shadowsocks 插件尚未适配",
	"unsupported_vless_encryption":        "VLESS 加密方式尚未适配",
	"unsupported_packet_encoding":         "UDP 数据包编码方式尚未适配",
	"unsupported_tls":                     "传输安全方式尚未适配",
	"invalid_reality":                     "REALITY 缺少有效公钥或认证参数",
	"unsupported_quic_fingerprint":        "QUIC 节点不能使用 TCP 指纹参数",
	"unsupported_reality_key_share":       "该 REALITY 密钥交换限制暂不能等价转换；原设置保留",
	"unsupported_ech":                     "当前传输安全方式不能使用该 ECH 配置",
	"invalid_ech":                         "ECH 配置格式异常；不会忽略 ECH 或降低证书校验",
	"invalid_ech_public_name":             "ECH 配置缺少有效公开域名；请让节点提供方修正 ECH 配置，原链接保留",
	"unsupported_hy2_obfs":                "HY2 混淆方式尚未适配",
	"invalid_port_range":                  "跳跃端口范围格式异常",
	"invalid_bandwidth":                   "带宽参数必须为正整数",
	"unsupported_protocol":                "节点协议尚未适配此内核",
	"unsupported_grpc_mode":               "gRPC 模式尚未适配",
	"unsupported_grpc_authority":          "自定义 gRPC 认证目标尚未适配",
	"unsupported_transport":               "节点传输方式尚未适配",
	"unsupported_transport_option":        "附加传输参数与当前传输方式不匹配",
	"unsupported_vmess_option":            "VMess 包含尚未支持的参数",
	"unsupported_vmess_alterid":           "仅支持 alterId 为 0 的 VMess AEAD",
	"unsupported_vmess_header":            "VMess 伪装头类型尚未适配",
	"unresolved_subscription":             "运行配置中仍有未解析的订阅",
	"unsupported_global_option":           "存在尚不能等价转换的全局选项，例如 MPTCP 或 TLS 分片",
	"unsupported_dns_option":              "DNS 响应规则、固定 TTL 或自定义缓存容量尚未适配",
	"unsupported_group_filter_annotation": "群组过滤器的附加设置尚未适配",
	"unsupported_group_filter":            "群组过滤方式尚未适配",
	"invalid_group_node":                  "群组引用了不存在或无效的节点",
	"empty_group":                         "群组中没有可用节点",
	"unsupported_group_policy":            "群组选择策略尚未适配",
	"unsupported_group_policy_parameter":  "群组选择策略的附加参数尚未适配",
	"invalid_fixed_group":                 "固定策略仅支持单个节点；原群组保持不变",
	"missing_check_url":                   "尚未配置连接测试地址",
	"invalid_check_interval":              "连接检查间隔格式异常",
	"unsupported_rule_target":             "路由规则的目标动作尚未适配",
	"unsupported_fallback":                "路由回退目标尚未适配",
	"unsupported_multi_wan":               "多个 WAN 接口尚不能等价转换",
	"missing_lan_interface":               "尚未配置需要接管的 LAN 接口",
	"geodata_missing":                     "找不到配置引用的规则库",
	"invalid_geodata_reference":           "规则库引用格式异常",
	"invalid_external_geodata":            "外部规则库引用格式异常；仅支持配置目录中的 .dat 文件名和条目名称",
	"unsupported_geoip":                   "IP 规则库匹配尚未适配",
	"unsupported_geosite":                 "域名规则库匹配尚未适配",
	"invalid_geodata":                     "规则库内容无效",
	"empty_geodata":                       "规则库引用未匹配到内容",
	"empty_rule":                          "路由规则没有可用的匹配条件",
	"unsupported_rule_parameter":          "路由匹配附加参数尚未适配",
	"unsupported_rule":                    "路由匹配方式尚未适配",
	"unsupported_domain_match":            "域名匹配方式尚未适配",
	"unsupported_dns_match":               "DNS 匹配方式尚未适配",
	"unsupported_source_geoip":            "来源地址的规则库匹配尚未适配",
	"invalid_ip_rule":                     "IP 匹配规则格式异常",
	"invalid_port_rule":                   "端口匹配规则格式异常",
	"unsupported_protocol_match":          "协议匹配方式尚未适配",
	"unsupported_ip_version":              "IP 版本匹配尚未适配",
	"unsupported_process_rule":            "进程匹配规则尚未适配",
	"unsupported_dns_upstream":            "DNS 上游格式异常或名称重复",
	"unsupported_dns_transport":           "DNS 上游传输方式尚未适配",
	"dns_upstream_requires_ip":            "DNS 上游地址需要使用 IP，以避免启动时循环解析",
	"invalid_dns_port":                    "DNS 上游端口格式异常",
	"unsupported_dns_detour":              "DNS 上游的代理出口尚未适配",
	"unsupported_dns_parameter":           "DNS 上游包含尚未适配的参数",
	"unsupported_dns_fallback":            "DNS 回退策略尚未适配",
	"unsupported_dns_target":              "DNS 规则引用的上游尚未适配",
	"invalid_dns_preference":              "DNS 地址族偏好格式异常",
}
