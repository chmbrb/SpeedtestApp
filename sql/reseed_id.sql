USE SpeedtestDb;
GO

DELETE FROM Speedtests;

DBCC CHECKIDENT ('Speedtests', RESEED, 0);