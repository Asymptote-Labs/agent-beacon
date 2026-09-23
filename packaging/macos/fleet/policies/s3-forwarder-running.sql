SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR EXISTS (
    SELECT 1 FROM processes
    WHERE path = '/opt/beacon/bin/vector'
      AND cmdline LIKE '%/Beacon/Forwarders/s3-vector.toml%'
  );
