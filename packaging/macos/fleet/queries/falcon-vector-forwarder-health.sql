SELECT
  CASE
    WHEN COUNT(*) = 0 THEN 'not_loaded'
    WHEN SUM(CASE WHEN disabled = 0 THEN 1 ELSE 0 END) > 0 THEN 'loaded'
    ELSE 'disabled'
  END AS falcon_vector_forwarder_health
FROM launchd
WHERE name = 'com.beacon.endpoint.falcon-forwarder';
