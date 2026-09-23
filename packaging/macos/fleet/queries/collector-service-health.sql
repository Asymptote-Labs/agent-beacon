SELECT
  CASE
    WHEN EXISTS (SELECT 1 FROM processes WHERE path = '/opt/beacon/bin/beacon-otelcol' AND uid = 0) THEN 'running'
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.collector.plist') THEN 'not_running'
    ELSE 'not_installed'
  END AS collector_service_health;
