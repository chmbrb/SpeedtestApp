USE SpeedtestDb;
GO

/* Declare variables*/
DECLARE @Mbps DECIMAL;
DECLARE @LocalTimezone NVARCHAR(64);

/* Set variables */
SET @Mbps = 125000.00;
SET @LocalTimezone = 'US Mountain Standard Time'

SELECT
    TRY_CONVERT(DATETIME2, JSON_VALUE(RawData, '$.timestamp'), 120) AT TIME ZONE 'UTC' AT TIME ZONE @LocalTimezone AS 'timestamp'
    , CASE 
        WHEN JSON_VALUE(RawData, '$.packetLoss') IS NULL THEN 0
        ELSE JSON_VALUE(RawData, '$.packetLoss')
      END AS 'packetLoss'
    , ROUND(CAST(JSON_VALUE(RawData, '$.download.bandwidth') / @Mbps AS FLOAT), 2) AS 'download'
    , ROUND(CAST(JSON_VALUE(RawData, '$.upload.bandwidth') / @Mbps AS FLOAT), 2) AS 'upload'
    , JSON_VALUE(RawData, '$.server.name') AS 'serverName'
    , JSON_VALUE(RawData, '$.server.location') AS 'serverLocation'
    , JSON_VALUE(RawData, '$.result.url') AS 'resultUrl'
    -- , JSON_VALUE(RawData, '$.') AS ''
    -- , JSON_VALUE(RawData, '$.') AS ''
    -- , JSON_VALUE(RawData, '$.') AS ''
FROM Speedtests;