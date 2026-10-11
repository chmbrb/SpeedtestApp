from pandas import DataFrame, Grouper, to_datetime

# [(idx, row.packetLoss, row.download, row.upload) for idx, row in df.iterrows()]
# df.resample('W').ffill()

class DataTransformer:
    
    _df:DataFrame

    def __init__(self, raw_data:list[tuple] = [()]):
        self._df = DataFrame(raw_data)

    def get_records(self, sample:str|None=None, start_date:str|None=None, end_date:str|None=None):
        stats, data = self._index_slice_sample(self._df, sample, start_date, end_date)
        return stats, data

    def _index_slice_sample(self, df:DataFrame, sample:str|None, start_date:str|None, end_date:str|None):
        _df = df.copy()
        _df['localtime'] = to_datetime(_df['localtime'])
        _df.set_index('localtime', inplace=True)
        if start_date:
            _df = _df.loc[f'{start_date}':]
        if end_date:
            _df = _df.loc[:f'{end_date}']
        if sample:
            _df = _df.resample(sample).ffill().dropna()
        stats = [(idx, row.packetLoss, row.download, row.upload)
                  for idx, row
                  in _df.describe().iterrows()]
        data = [(idx, row.packetLoss, row.download, row.upload) 
                 for idx, row
                 in _df.iterrows()]
        return stats, data