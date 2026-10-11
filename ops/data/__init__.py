from os import getenv
from dotenv import load_dotenv
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker, scoped_session


load_dotenv()

# e.g. postgresql+psycopg://user:password@host:5432/speedtestdb
# (same variable the Flask app uses)
engine = create_engine(getenv("SQLALCHEMY_DATABASE_URI"))
session = scoped_session(sessionmaker(bind=engine))
