from typing import Any
from flaskapp.data_access import speedtest_dao
from .BaseService import BaseService


class SpeedtestService(BaseService):

    def __init__(self, dao=speedtest_dao):
        super().__init__(dao)
    
    def get_all(self) -> Any:
        return self._dao.get_all()


speedtest_service = SpeedtestService()