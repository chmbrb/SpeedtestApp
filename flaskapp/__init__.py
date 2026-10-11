from flask import Flask
from werkzeug.middleware.proxy_fix import ProxyFix
from flaskapp.data import db
from flaskapp.config import Config


def create_app():
    '''Generic Flask Application Factory Pattern'''

    from flaskapp.routes.home import home
    from flaskapp.routes.speedtest import speedtest
    from flaskapp.routes.health_check import health_check

    app = Flask(__name__)

    app.register_blueprint(home)
    app.register_blueprint(speedtest)
    app.register_blueprint(health_check)

    app.config.from_object(Config)

    db.init_app(app)

    # additional configuration options
    if (app.config['USE_PROXY']):
        app.wsgi_app = ProxyFix(app.wsgi_app, x_for=1, x_proto=1, x_host=1, x_prefix=1)

    return app

app = create_app()