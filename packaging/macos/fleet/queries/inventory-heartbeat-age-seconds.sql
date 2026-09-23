SELECT COALESCE(
  (SELECT CAST(CAST(strftime('%s', 'now') AS INTEGER) - mtime AS TEXT)
     FROM file
     WHERE path = '/var/log/beacon-agent/inventory_state.jsonl'),
  'missing'
) AS inventory_heartbeat_age_seconds;
