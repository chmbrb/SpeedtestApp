from os import getenv
from dotenv import load_dotenv
from sqlalchemy import create_engine
from sqlalchemy.engine import URL
from sqlalchemy.orm import sessionmaker, scoped_session


load_dotenv()

connection_url = URL.create(
    "mssql+pyodbc",
    username=getenv("USERNAME"),
    password=getenv("PASSWORD"),
    host=getenv("SERVERNAME"),
    port=int(getenv("SERVERPORT")),
    database=getenv("DB"),
    query={
        'driver': getenv("DRIVER"),
        'TrustServerCertificate': getenv("TRUSTSERVERCERTIFICATE")
    }
)

# print(connection_url)

engine = create_engine(connection_url)
session = scoped_session(sessionmaker(bind=engine))