SET ANSI_NULLS ON
GO
SET QUOTED_IDENTIFIER ON
GO

-- SQL Server version as deployed (note: @LocalTimezone is ignored; zone is hard-coded)
CREATE PROCEDURE [dbo].[spSpeedtestResults_GetAll]
    @LocalTimezone  NVARCHAR(64)
AS
BEGIN
    DECLARE @Mbps DECIMAL = 125000.00

    SELECT
        FORMAT(TRY_CONVERT(DATETIME2,
                JSON_VALUE(RawData, '$.timestamp'), 120)
                AT TIME ZONE 'UTC'
                AT TIME ZONE 'US Mountain Standard Time', 'yyyy-MM-dd HH:mm:ss')
                AS 'localtime'
        , CASE 
            WHEN JSON_VALUE(RawData, '$.packetLoss') IS NULL THEN 0
            ELSE ROUND(CAST(JSON_VALUE(RawData, '$.packetLoss') AS FLOAT), 4)
        END AS 'packetLoss'
        , ROUND(CAST(JSON_VALUE(RawData, '$.download.bandwidth') / @Mbps AS FLOAT), 2) AS 'download'
        , ROUND(CAST(JSON_VALUE(RawData, '$.upload.bandwidth') / @Mbps AS FLOAT), 2) AS 'upload'
    FROM Speedtests;
END
GO
