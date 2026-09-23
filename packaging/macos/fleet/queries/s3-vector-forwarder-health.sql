SELECT
  CASE
    WHEN EXISTS (
      SELECT 1 FROM processes
      WHERE path = '/opt/beacon/bin/vector'
        AND cmdline LIKE '%/Beacon/Forwarders/s3-vector.toml%'
    ) THEN 'running'
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.s3-forwarder.plist') THEN 'not_running'
    ELSE 'not_configured'
  END AS s3_vector_forwarder_health;
