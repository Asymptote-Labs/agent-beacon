SELECT
  CASE
    WHEN (SELECT COUNT(*) FROM file
          WHERE path = '/Library/Application Support/Beacon/Endpoint/config.json') = 0
      THEN 'missing'
    ELSE COALESCE(
      (SELECT GROUP_CONCAT(pj.value, ',')
       FROM parse_json pj
       WHERE pj.path = '/Library/Application Support/Beacon/Endpoint/config.json'
         AND pj.parent = 'harnesses'),
      'unknown')
  END AS configured_harnesses;
