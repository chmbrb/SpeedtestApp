import json
import subprocess
from sqlalchemy import text
from ops.data import session


class ProcessHandler:
    _session = session

    def exec_command(self, cmd:str) -> str:
        proc = subprocess.run(cmd.split(), capture_output=True, text=True)
        if proc.returncode != 0:
            raise RuntimeError(f"command failed ({proc.returncode}): {proc.stderr.strip()}")
        return proc.stdout

    def insert_speedtest(self, raw:str):
        """Store one Ookla CLI JSON result. Raises on bad JSON or database errors."""
        json.loads(raw)  # fail early with a clear error if the CLI output isn't JSON
        sess = self._session()
        try:
            sess.execute(text("SELECT speedtests_insert(CAST(:d AS jsonb))"), {"d": raw})
            sess.commit()
        except Exception:
            sess.rollback()
            raise
        finally:
            sess.close()

    def run_speedtest(self, cmd:str):
        self.insert_speedtest(self.exec_command(cmd))
