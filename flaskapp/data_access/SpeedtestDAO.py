from sqlalchemy import text
from typing import Any
from flaskapp.data import db
from flaskapp.config import Config


class SpeedtestDAO:
    '''Speedtest Data Access Object'''

    def get_all(self) -> Any:
        # TIMEZONE must be an IANA name, e.g. America/Phoenix
        tz = Config.TIMEZONE or 'America/Phoenix'
        return db.session.execute(
            text("SELECT * FROM speedtest_results(:tz)"),
            {"tz": tz}
        ).all()


speedtest_dao = SpeedtestDAO()
