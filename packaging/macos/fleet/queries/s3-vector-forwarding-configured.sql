SELECT
  CASE
    WHEN COUNT(*) = 0 THEN 'not_configured'
    ELSE 'configured'
  END AS s3_vector_forwarding_state
FROM file
WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.env';
