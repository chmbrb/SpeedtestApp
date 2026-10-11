from typing import Any

class BaseService:
    '''Default Partial Base Service Class'''
    _dao: Any

    def __init__(self, dao:Any=None):
        self._dao = dao