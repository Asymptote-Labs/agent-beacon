SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR (
    EXISTS (SELECT 1 FROM file WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.env' AND size > 0)
    AND EXISTS (SELECT 1 FROM file WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.toml')
    AND EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.s3-forwarder.plist')
  );
