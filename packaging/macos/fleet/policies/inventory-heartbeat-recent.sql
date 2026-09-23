SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR EXISTS (
    SELECT 1 FROM file
    WHERE path = '/var/log/beacon-agent/inventory_state.jsonl'
      AND mtime >= CAST(strftime('%s', 'now') AS INTEGER) - 345600
  );
