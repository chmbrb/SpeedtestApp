#!/usr/bin/env python3
"""Export dbo.Speedtests from SQL Server to CSV (id, created_utc, raw_data).

Usage: pipenv run python scripts/export_speedtests.py '<sqlalchemy url>' [out.csv]
  e.g. 'mssql+pyodbc://user:pass@host:1433/SpeedtestDb?TrustServerCertificate=yes&driver=ODBC+Driver+18+for+SQL+Server'
Read-only: only issues a SELECT. Quote the URL in single quotes (zsh expands '!' in double quotes).
Special characters in the password (@ : / ? #) must be percent-encoded in the URL.
"""
import csv
import sys
from sqlalchemy import create_engine, text

url = sys.argv[1]
out_path = sys.argv[2] if len(sys.argv) > 2 else "speedtests_export.csv"

engine = create_engine(url)
with engine.connect() as conn, open(out_path, "w", newline="", encoding="utf-8") as f:
    w = csv.writer(f)
    count = 0
    result = conn.execute(text("SELECT Id, Created, CAST(RawData AS NVARCHAR(MAX)) AS RawData FROM Speedtests ORDER BY Id"))
    for row in result:
        w.writerow([row.Id, row.Created.strftime("%Y-%m-%d %H:%M:%S.%f") + "+00", row.RawData])
        count += 1
print(f"exported {count} rows to {out_path}")
