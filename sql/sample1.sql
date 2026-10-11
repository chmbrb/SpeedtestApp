-- USE SpeedtestDb;
-- GO


/* Declare variables*/
DECLARE @Mbps DECIMAL = 125000.00;
DECLARE @LocalTimezone NVARCHAR(64) = 'US Mountain Standard Time';
DECLARE @Period NVARCHAR(16) = 'yearly';
DECLARE @PeriodStmt NVARCHAR(32);
DECLARE @PeriodValue INT = 2026;
DECLARE @stmt NVARCHAR(MAX);
DECLARE @line NVARCHAR(2) = NCHAR(13) + NCHAR(10);
DECLARE @tab NCHAR(1) = NCHAR(9);

-- perform as daily sql job at midnight
IF OBJECT_ID('dbo.SpeedtestResults', 'U') IS NOT NULL
    DROP TABLE dbo.SpeedtestResults;


SELECT
    TRY_CONVERT(DATETIME2, JSON_VALUE(RawData, '$.timestamp'), 120) AT TIME ZONE 'UTC' AT TIME ZONE @LocalTimezone AS 'localtime'
    , CASE 
        WHEN JSON_VALUE(RawData, '$.packetLoss') IS NULL THEN 0
        ELSE JSON_VALUE(RawData, '$.packetLoss')
    END AS 'packetLoss'
    , ROUND(CAST(JSON_VALUE(RawData, '$.download.bandwidth') / @Mbps AS FLOAT), 2) AS 'download'
    , ROUND(CAST(JSON_VALUE(RawData, '$.upload.bandwidth') / @Mbps AS FLOAT), 2) AS 'upload'
INTO SpeedtestResults
FROM Speedtests;

-- SELECT * FROM SpeedtestResults;

-- DELETE FROM SpeedtestResults
-- WHERE YEAR(localtime) != 

-- select getutcdate() AT TIME ZONE 'UTC' AT TIME ZONE @LocalTimezone as localtime
-- select getdate()

-- SELECT YEAR(localTime)
-- FROM SpeedtestResults

-- IF @Period = 'yearly'
--     SET @PeriodStmt = 'YEAR(localtime)';
-- ELSE IF @Period = 'monthly'
--     SET @PeriodStmt = 'MONTH(localtime)';
-- ELSE IF @Period = 'weekly'
--     SET @PeriodStmt = 'WEEK(localtime)';
-- ELSE IF @Period = 'daily'
--     SET @PeriodStmt = 'DAY(localtime)'

-- SELECT @PeriodStmt

-- SET @stmt = 'SELECT *' + @line + 
--             'FROM SpeedtestResults' + @line +
--             'WHERE ' + @PeriodStmt + ' = ' + CAST(@PeriodValue AS VARCHAR(4)) + ';'

-- SELECT @stmt
-- EXEC(@stmt);

-- SELECT * FROM SpeedtestResults;
-- IF @PeriodValue IS NULL
-- SELECT 1

-- IF @PeriodValue IS NULL
-- BEGIN
--     SET @stmt = 'SELECT * FROM SpeedtestResults;'
-- END
-- ELSE
-- BEGIN
--     SET @stmt
-- END

-- EXEC(@stmt)
-- SELECT * 
-- FROM SpeedtestResults
-- WHERE MONTH(localTime) = MONTH(CAST(GETDATE() AS DATE));



-- SELECT YEAR(CAST(GETDATE() AS DATE))