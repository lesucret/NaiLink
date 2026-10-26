#!/bin/sh
# SPDX-License-Identifier: MIT
# Inspect the current datapath and make bounded public connectivity requests.
# Never change UCI, services, routing, firewall, qdiscs or the NaiLink database.
set +x

for tool in ubus jsonfilter; do
	command -v "$tool" >/dev/null 2>&1 || { echo "Missing command: $tool"; exit 1; }
done
printf 'NaiLink network check v2 (configuration read-only)\n'
echo 'traffic_owner=unknown; HTTPS success tests the current path, not NaiLink specifically.'
echo 'Service records, processes and firewall markers are observations, not proof of traffic ownership.'
printf 'time='; date -Iseconds
printf 'kernel='; uname -r
printf 'package='; cat /usr/share/daed-router/version 2>/dev/null || echo unknown

lan=$(uci -q get daed.main.lan_network 2>/dev/null) || lan=lan
case "$lan" in ''|*[!a-zA-Z0-9_]*) lan=lan ;; esac
devices=''
printf '\n[Logical interfaces and actual devices]\n'
for network in "$lan" wan wan6; do
	state=$(ubus call "network.interface.$network" status 2>/dev/null) || state='{}'
	device=$(printf '%s' "$state" | jsonfilter -e '@.l3_device' 2>/dev/null)
	up=$(printf '%s' "$state" | jsonfilter -e '@.up' 2>/dev/null)
	printf 'network=%s up=%s device=%s\n' "$network" "${up:-unknown}" "${device:-unknown}"
	case "$device" in ''|*[!a-zA-Z0-9_.:@-]*) ;; *) devices="$devices $device" ;; esac
done
if command -v ip >/dev/null 2>&1; then
	printf '\n[Default routes]\n'
	ip -4 route show default 2>/dev/null | head -n 8
	ip -6 route show default 2>/dev/null | head -n 8
fi

service_snapshot() {
	printf '\n[Service/process snapshot: %s]\n' "$1"
	date -Iseconds
	services_ok=yes
	services=$(ubus call service list 2>/dev/null) || services_ok=no
	for service in daed dae passwall passwall2 openclash mihomo sing-box; do
		active=$(printf '%s' "$services" | jsonfilter -e "@[\"$service\"].instances.*.running" 2>/dev/null)
		case "$services_ok:$active" in
			no:*) state=unavailable ;;
			yes:*true*) state=running ;;
			yes:*false*) state=stopped ;;
			*) state=unreported ;;
		esac
		printf 'service=%s procd=%s\n' "$service" "$state"
	done
	echo 'unreported means no instance state was returned; it does not mean the proxy is stopped.'
	if command -v pidof >/dev/null 2>&1; then
		for core in daed dae xray v2ray sing-box mihomo clash hysteria hysteria2 trojan tuic-client ss-redir ss-local sslocal naive; do
			if pidof "$core" >/dev/null 2>&1; then state=present; else state=not-found; fi
			printf 'process=%s state=%s\n' "$core" "$state"
		done
		echo 'Core names can be shared by several plugins; not-found does not rule out renamed processes.'
	else
		echo 'pidof unavailable; core process inspection skipped'
	fi
}

service_snapshot before-probes

printf '\n[Local NaiLink readiness and automatic selection candidates]\n'
listen=$(/usr/libexec/daed/check --listen 2>/dev/null) || listen=''
if [ -n "$listen" ] && command -v uclient-fetch >/dev/null 2>&1; then
	status=$(uclient-fetch -q -T 3 -O - "http://$listen/health/ready?details=1" 2>/dev/null) || status='{}'
	for field in core phase configured listening initialized dns_ready routing_ready restart_dns restored_dns_entries; do
		value=$(printf '%s' "$status" | jsonfilter -e "@.$field" 2>/dev/null)
		printf '%s=%s\n' "$field" "${value:-unavailable}"
	done
	# The local endpoint only returns bounded labels, no node URLs or passwords.
	printf '%s' "$status" | jsonfilter -e '@.groups[*]' 2>/dev/null | head -n 64
else
	echo 'readiness unavailable; older runtime or local API not running'
fi
echo 'Candidates describe the current automatic policy, not the public IP seen by a website.'
echo 'An automatic group may select a different exit after restart; its configured policy was not changed.'

printf '\n[Firewall text markers; counts only, no rules or addresses printed]\n'
for firewall_tool in nft iptables-save ip6tables-save; do
	if ! command -v "$firewall_tool" >/dev/null 2>&1 || ! command -v timeout >/dev/null 2>&1; then
		printf '%s unavailable; marker inspection skipped\n' "$firewall_tool"
		continue
	fi
	{
		if [ "$firewall_tool" = nft ]; then
			timeout 6 nft list ruleset
		else
			timeout 6 "$firewall_tool"
		fi && printf '\nDAED_CHECK_RULES_READ_OK\n'
	} 2>/dev/null | awk -v tool="$firewall_tool" '
		$0 == "DAED_CHECK_RULES_READ_OK" { ok=1; next }
		{ line=tolower($0); if (line ~ /passwall|psw2?_/) pw++; if (line ~ /openclash/) oc++ }
		END {
			if (!ok) print tool " query=unavailable (failed or timed out)"
			else printf "%s passwall_marker_lines=%d openclash_marker_lines=%d\n", tool, pw, oc
		}'
done
echo 'Markers may be stale; zero markers does not exclude a proxy. Rules were not changed.'

printf '\n[Existing traffic-control filters; no attachment changes]\n'
if command -v tc >/dev/null 2>&1; then
	for device in $devices; do
		printf 'device=%s ingress\n' "$device"
		tc filter show dev "$device" ingress 2>/dev/null | head -n 12
		printf 'device=%s egress\n' "$device"
		tc filter show dev "$device" egress 2>/dev/null | head -n 12
	done
else
	echo 'tc unavailable; filter inspection skipped'
fi

read_logs() {
	ubus call daed-router logs 2>/dev/null | jsonfilter -e '@.logs' 2>/dev/null | sed -E \
		-e 's#[a-zA-Z][a-zA-Z0-9+.-]*://[^[:space:]"<>]+#[URL REDACTED]#g' \
		-e 's/([Tt]oken|[Pp]assword|[Aa]uthorization)([=: ]+)[^, ]+/\1\2[REDACTED]/g' \
		-e 's/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/[UUID REDACTED]/g'
}

diagnostic_lines() {
	grep -Ei 'reload|bind to (lan|wan)|dial|dns|timeout|ready|level=(error|warning)' | tail -n 70
}

logs_before=$(read_logs)
printf '\n[History captured BEFORE probes; not evidence of current connectivity]\n'
if [ -n "$logs_before" ]; then
	printf '%s\n' "$logs_before" | diagnostic_lines
else
	echo 'Log baseline unavailable through the installed RPC; no raw configuration was read'
fi
printf '\nprobe_start='; date -Iseconds
printf '\n[Google DNS through the local resolver; 6 seconds maximum]\n'
if command -v nslookup >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1; then
	timeout 6 nslookup www.google.com 127.0.0.1 2>&1 | head -n 24
else
	echo 'nslookup/timeout unavailable; DNS probe skipped'
fi

printf '\n[Router HTTPS; transparent routing remains in effect]\n'
if command -v curl >/dev/null 2>&1; then
	for target in cn4 google4 google6 chatgpt4 chatgpt6; do
		case "$target" in cn4) family=-4; url=https://www.baidu.com/ ;; google4) family=-4; url=https://www.google.com/ ;; google6) family=-6; url=https://www.google.com/ ;; chatgpt4) family=-4; url=https://chatgpt.com/ ;; chatgpt6) family=-6; url=https://chatgpt.com/ ;; esac
		printf '%s ' "$target"
		curl "$family" --silent --show-error --head --noproxy '*' \
			--connect-timeout 4 --max-time 8 --output /dev/null \
			--write-out 'http=%{http_code} peer=%{remote_ip} dns=%{time_namelookup} connect=%{time_connect} tls=%{time_appconnect}\n' \
			"$url" 2>&1
		printf 'curl_exit=%s\n' "$?"
	done
	echo 'peer is the destination address, not your public exit IP. HTTP 403 may be a site policy or challenge, not a failed TLS connection.'
else
	echo 'curl unavailable; HTTPS probes skipped (nothing installed)'
fi

printf '\nprobe_end='; date -Iseconds
logs_after=$(read_logs)
service_snapshot after-probes
printf '\n[Log changes during probes; credentials redacted, request ownership unverified]\n'
if [ -z "$logs_after" ] || [ -z "$logs_before" ]; then
	echo 'log_continuity=unknown; one or both log snapshots unavailable'
elif [ "$logs_before" = "$logs_after" ]; then
	echo 'new_log_lines=0; history above is not a trace of these probes'
else
	baseline_lines=$(printf '%s\n' "$logs_before" | wc -l)
	printf '%s\n%s\n' "$logs_before" "$logs_after" | awk -v n="$baseline_lines" '
		NR <= n { before[NR]=$0; next }
		{ after[++m]=$0 }
		END {
			# RPC returns a bounded tail: allow its oldest lines to drop out.
			limit=n<m?n:m
			for (overlap=limit; overlap>0; overlap--) {
				same=1
				for (i=1; i<=overlap; i++) {
					if (before[n-overlap+i] != after[i]) { same=0; break }
				}
				if (same) break
			}
			if (!overlap) {
				print "log_continuity=unknown; log rotated, reset or changed; no lines classified as new"
				exit
			}
			printf "new_log_lines=%d (appended since baseline; may include background traffic)\n", m-overlap
			for (i=overlap+1; i<=m; i++) {
				if (tolower(after[i]) ~ /reload|bind to (lan|wan)|dial|dns|timeout|ready|level=(error|warning)/) print after[i]
			}
		}' | tail -n 71
fi
echo 'traffic_owner=unknown; these observations alone cannot validate NaiLink forwarding.'
printf '\nFinished. No configuration, service, routing or firewall changes were made.\n'
