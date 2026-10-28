// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"os"
	"strconv"
	"strings"
)

// RSS is resident process memory, not reserved virtual address space or Linux
// file cache. Shared pages can appear in both processes, so the sum is an estimate.
type routerCoreMemory struct {
	BackendKiB uint64  `json:"backend_kib"`
	NativeKiB  *uint64 `json:"native_kib,omitempty"`
	TotalKiB   uint64  `json:"total_kib"`
	Complete   bool    `json:"complete"`
}

func routerParseResidentKiB(data []byte) (uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "VmRSS:" && fields[2] == "kB" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			return value, err == nil && value <= 1<<50
		}
	}
	return 0, false
}

func routerResidentKiB(pid int) (uint64, bool) {
	if pid < 1 {
		return 0, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	return routerParseResidentKiB(data)
}

func routerCoreResidentMemory(active routerCoreSession, name string) *routerCoreMemory {
	backend, ok := routerResidentKiB(os.Getpid())
	if !ok {
		return nil
	}
	result := &routerCoreMemory{BackendKiB: backend, TotalKiB: backend, Complete: name == "dae"}
	if native, ok := active.(*routerSingBoxSession); ok {
		s := native.current()
		select {
		case <-s.exit:
			return result
		default:
		}
		if s.cmd != nil && s.cmd.Process != nil {
			if resident, ok := routerResidentKiB(s.cmd.Process.Pid); ok {
				result.NativeKiB = &resident
				result.TotalKiB += resident
				result.Complete = !s.hasTor
			}
		}
	}
	return result
}
