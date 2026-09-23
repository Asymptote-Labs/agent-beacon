SELECT COALESCE(
  (SELECT version FROM package_receipts WHERE package_id = 'ai.asymptote.beacon.endpoint'),
  CASE
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/opt/beacon/bin/beacon') THEN 'installed_without_receipt'
    ELSE 'not_installed'
  END
) AS beacon_version;
