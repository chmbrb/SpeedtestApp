import subprocess
from sqlalchemy import text
from ops.data import session


class ProcessHandler:
    _session = session

    def __init__(self):
        pass

    def exec_command(self, cmd:str):
        return subprocess.run(cmd.split(), capture_output=True, text=True).stdout
    
    def exec_sp(self, sp:str, data=None, read:bool=True):
        sess = self._session()
        result = None
        try:
            if (data):
                    query = text(f"{sp} :d")
                    if (read):
                        result = sess.execute(query, {"d": data}).all()
                    else:
                        sess.execute(query, {"d": data})
                        sess.commit()    
            else:
                    result = sess.execute(text(sp)).all()
        except Exception as e:
            print(e)
        finally:
            sess.close()
            return result
        
    def run_speedtest(self, cmd:str):
        result = self.exec_command(cmd)
        self.exec_sp("spSpeedtests_Insert", result, False)