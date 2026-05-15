SELECT
  CASE
    WHEN NOT EXISTS (
      SELECT 1
      FROM file
      WHERE path = '/Library/Application Support/Beacon/Endpoint/config.json'
    ) THEN 'missing'
    WHEN COUNT(*) > 0 THEN 'configured'
    ELSE 'not_configured'
  END AS splunk_hec_config_state
FROM yara
WHERE path = '/Library/Application Support/Beacon/Endpoint/config.json'
  AND sigrule = 'rule splunk_hec_configured { strings: $dest = "\"splunk_hec\"" $endpoint = "\"endpoint\"" condition: all of them }';
