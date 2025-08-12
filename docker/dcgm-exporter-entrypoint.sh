#!/usr/bin/env bash
set -euo pipefail

ldconfig #Must refresh the cache to find libdcgm.so
# We want to setcap only when the container is started with the right caps
DCGM_EXPORTER=$(readlink -f $(which dcgm-exporter))
if [ -z "$NO_SETCAP" ]; then
   if setcap 'cap_sys_admin=+ep' $DCGM_EXPORTER; then
      if ! $DCGM_EXPORTER -v 1>/dev/null 2>/dev/null; then
         >&2 echo "Warning #2: dcgm-exporter doesn't have sufficient privileges to expose profiling metrics. To get profiling metrics with dcgm-exporter, use --cap-add SYS_ADMIN"
         setcap 'cap_sys_admin=-ep' $DCGM_EXPORTER
      fi
   else
      >&2 echo "Warning #1: dcgm-exporter doesn't have sufficient privileges to expose profiling metrics. To get profiling metrics with dcgm-exporter, use --cap-add SYS_ADMIN"
   fi

fi

# Pass the command line arguments to dcgm-exporter
set -- $DCGM_EXPORTER "$@"
exec "$@"
