SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR (
    EXISTS (SELECT 1 FROM package_receipts WHERE package_id = 'ai.asymptote.beacon.endpoint')
    AND EXISTS (SELECT 1 FROM file WHERE path = '/opt/beacon/bin/beacon')
  );
