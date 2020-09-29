#!/usr/bin/env bash
set -euo pipefail

# We want to setcap only when the container is started with the right permissions
DCGM_EXPORTER=$(readlink -f $(which dcgm-exporter))
if [ -z "$NO_SETCAP" ]; then
   setcap 'cap_sys_admin=+ep' $DCGM_EXPORTER

   if ! $DCGM_EXPORTER -v 1>/dev/null 2>/dev/null; then
      >&2 echo "dcgm-exporter doesn't have sufficient privileges to expose profiling metrics. To use dcgm-exporter for profiling metrics use --cap-add SYS_ADMIN"
      setcap 'cap_sys_admin=-ep' $DCGM_EXPORTER
   fi
fi

# Pass the command line arguments to dcgm-exporter
set -- $DCGM_EXPORTER "$@"
exec "$@"
