// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"encoding/json"
	"fmt"
	"github.com/daeuniverse/dae-wing/routercore"
	daeCommon "github.com/daeuniverse/dae/common"
	daeConfig "github.com/daeuniverse/dae/config"
)

func RouterCoreCapabilities() string {
	engines := routercore.ProtocolCapabilities()
	_, extraErr := routercore.ExtraDirectory(routerExtraRoot)
	if extraErr != nil {
		for i := range engines {
			if engines[i].ID == "sing-box" {
				for j := range engines[i].Protocols {
					p := &engines[i].Protocols[j]
					if p.Name == "Naive HTTPS" || p.Name == "WireGuard" || p.Name == "Tor" {
						p.Note = "扩展协议包尚未安装。" + p.Note
					}
				}
			}
		}
	}
	data, _ := json.Marshal(map[string]any{"current": RouterCoreName(), "engines": engines, "extra_installed": extraErr == nil})
	return string(data)
}
func routerDaeNodeProblems(conf *daeConfig.Config) []string {
	var p []string
	for i, n := range conf.Node {
		_, link := daeCommon.GetTagFromLinkLikePlaintext(string(n))
		if routercore.IsNativeURI(link) {
			p = append(p, fmt.Sprintf("节点 %d：sing-box 原生节点仅能在 sing-box 中运行。原节点保留，请先从 dae 运行群组移除此节点。", i+1))
		}
	}
	return p
}
