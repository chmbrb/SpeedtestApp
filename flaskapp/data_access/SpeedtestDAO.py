from sqlalchemy import text
from typing import Any
from flaskapp.data import db
from flaskapp.config import Config


class SpeedtestDAO:
    '''Speedtest Data Access Object'''

    # def __init__(self):
    #     super().__init__()

    def get_all(self, sp:str='spSpeedtestResults_GetAll') -> Any:
        sp = "spSpeedtestResults_GetAll"
        tz = Config.TIMEZONE
        try:
            query = text(f"{sp} :d")
            return (
                db.session.execute(
                    query,
                    {"d": tz}
                ).all()
            )
        except Exception as e:
            print(e)



speedtest_dao = SpeedtestDAO()