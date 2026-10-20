#!/bin/sh
# Atalaya plugin: free space on the disk that holds your home.
#
#   [[plugin]]
#   name = "disco"
#   command = ["~/ruta/a/atalaya/examples/plugins/disk.sh"]
#   interval = "5m"
set -eu
df -kP "$HOME" | awk 'NR == 2 {
	pct = $5 + 0
	status = "ok"
	if (pct >= 80) status = "warn"
	if (pct >= 90) status = "error"
	printf "{\"title\":\"Disco\",\"summary\":\"%d%% usado\",\"columns\":[\"VOLUMEN\",\"USADO\",\"LIBRE\"],", pct
	printf "\"rows\":[{\"cells\":[\"%s\",\"%d%%\",\"%.1f GB\"],\"status\":\"%s\"}]}\n", $6, pct, $4 / 1048576, status
}'
