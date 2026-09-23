SELECT
  CASE
    WHEN EXISTS (
      SELECT 1 FROM processes
      WHERE path = '/opt/beacon/bin/vector'
        AND cmdline LIKE '%/Beacon/Forwarders/falcon-vector.toml%'
    ) THEN 'running'
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.falcon-forwarder.plist') THEN 'not_running'
    ELSE 'not_configured'
  END AS falcon_vector_forwarder_health;
