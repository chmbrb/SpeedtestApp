from os import path, getenv
from dotenv import load_dotenv

basedir = path.abspath(path.dirname(__file__))
load_dotenv(path.join(basedir, '.env'))

# https://hackersandslackers.com/configure-flask-applications/
class Config:
    '''Application Configuration Settings'''
    # general config
    FLASK_ENV = getenv("FLASK_ENV")
    FLASK_APP = getenv("FLASK_APP")
    FLASK_DEBUG = getenv("FLASK_DEBUG")
    # FLASK_SERVER_HOST = getenv("FLASK_SERVER_HOST")
    FLASK_SERVER_PORT = getenv("FLASK_SERVER_PORT")
    # ALLOW_REGISTER = getenv("ALLOW_REGISTER")
    # DRY_RUN = getenv("DRY_RUN")
    # SERVER_NAME = getenv("SERVER_NAME")
    # SECRET_KEY = getenv("SECRET_KEY")
    STATIC_FOLDER = "static"
    TEMPLATES_FOLDER = "templates"

  
    SQLALCHEMY_DATABASE_URI = getenv('SQLALCHEMY_DATABASE_URI')
    # SQLALCHEMY_DATABASE_URI = path.join(basedir, 'data', getenv("SQLALCHEMY_DATABASE_URI"))
    # SQLALCHEMY_TRACK_MODIFICATIONS = False
    TIMEZONE = getenv('TIMEZONE')

    # run app behind proxy
    USE_PROXY = getenv("USE_PROXY")

    # use ASGI; (not implemented since most of the current codebase is not written asyncronously!)
    # TODO: update codebase; use uvicorn for serving by reverse proxy
    # USE_ASGI = getenv("USE_ASGI")
