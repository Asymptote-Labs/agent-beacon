SELECT
  CASE
    WHEN (SELECT COUNT(*) FROM file
          WHERE path = '/Library/Application Support/Beacon/Endpoint/config.json') = 0
      THEN 'missing'
    ELSE COALESCE(
      (SELECT pj.value
       FROM parse_json pj
       WHERE pj.path = '/Library/Application Support/Beacon/Endpoint/config.json'
         AND pj.key = 'content_retention'
       LIMIT 1),
      'unknown')
  END AS content_retention;
