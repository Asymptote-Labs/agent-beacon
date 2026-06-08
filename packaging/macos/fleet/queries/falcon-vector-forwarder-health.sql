SELECT
  CASE
    WHEN COUNT(*) = 0 THEN 'not_loaded'
    WHEN SUM(CASE WHEN pid > 0 THEN 1 ELSE 0 END) > 0 THEN 'running'
    ELSE 'loaded_not_running'
  END AS falcon_vector_forwarder_health
FROM launchd
WHERE label = 'com.beacon.endpoint.falcon-forwarder';
