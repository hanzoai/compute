#!/bin/bash
# All-in-one entrypoint (ALLINONE Dockerfile target)
# Credentials must be provided via environment variables.

if [ -z "${DB_PASSWORD}" ]; then
  echo "ERROR: DB_PASSWORD environment variable is required" >&2
  exit 1
fi

service mariadb start
mysqladmin -u root password "${DB_PASSWORD}"

/opt/guacamole/sbin/guacd -b 0.0.0.0 -L "$GUACD_LOG_LEVEL"

exec /visor --createDatabase=true
