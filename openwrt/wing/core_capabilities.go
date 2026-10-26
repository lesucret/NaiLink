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
	data, _ := json.Marshal(map[string]any{"current": RouterCoreName(), "engines": routercore.ProtocolCapabilities()})
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
